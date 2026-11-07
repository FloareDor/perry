package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/perry-dev/perry/internal/object"
	"github.com/perry-dev/perry/internal/pack"
	"github.com/perry-dev/perry/internal/store"
)

// Capabilities advertised for receive-pack.
const receivePackCaps = "report-status side-band-64k delete-refs ofs-delta agent=" + Agent

// RefCommand is one reference change a client is asking for.
type RefCommand struct {
	Old  object.ID
	New  object.ID
	Name string
}

// IsCreate reports whether the command creates a reference.
func (c RefCommand) IsCreate() bool { return c.Old.IsZero() }

// IsDelete reports whether the command removes a reference.
func (c RefCommand) IsDelete() bool { return c.New.IsZero() }

// PushRequest is a parsed receive-pack request: the reference changes, the
// client's capabilities, and a reader positioned at the packfile.
type PushRequest struct {
	Commands     []RefCommand
	Capabilities map[string]string
	Pack         io.Reader
}

// HasCap reports whether the client asked for a capability.
func (r *PushRequest) HasCap(name string) bool {
	_, ok := r.Capabilities[name]
	return ok
}

// ParsePushRequest reads the command list, leaving the reader positioned at
// the packfile that follows.
//
// Each command is "<old> <new> <ref>", with the first carrying capabilities
// after a NUL. A push that only deletes references sends no packfile, so
// the absence of pack data is not an error.
func ParsePushRequest(r io.Reader) (*PushRequest, error) {
	req := &PushRequest{Capabilities: map[string]string{}}
	pr := NewReader(r)

	for {
		kind, payload, err := pr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if kind == KindFlush {
			break
		}
		if kind != KindData {
			continue
		}

		line := strings.TrimSuffix(string(payload), "\n")
		if nul := strings.IndexByte(line, 0); nul >= 0 {
			parseCaps(line[nul+1:], req.Capabilities)
			line = line[:nul]
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("receive-pack: malformed command %q", line)
		}
		oldID, err := object.ParseID(fields[0])
		if err != nil {
			return nil, fmt.Errorf("receive-pack: bad old id: %w", err)
		}
		newID, err := object.ParseID(fields[1])
		if err != nil {
			return nil, fmt.Errorf("receive-pack: bad new id: %w", err)
		}
		req.Commands = append(req.Commands, RefCommand{Old: oldID, New: newID, Name: fields[2]})
	}

	// Everything after the flush is packfile, still buffered inside the
	// pkt-line reader, so hand back a reader that drains that first.
	req.Pack = pr.Rest()
	return req, nil
}

// CommandResult is the outcome of one reference change.
type CommandResult struct {
	Name string
	Err  error // nil on success
}

// ReceivePack stores a pushed packfile and applies the reference changes.
//
// Objects are written before any reference moves. A reference that pointed
// at an object the store did not hold would be a broken repository, so the
// order matters: an interrupted push leaves unreferenced objects, which is
// recoverable, rather than dangling references, which is not.
func ReceivePack(ctx context.Context, s store.Store, req *PushRequest, w io.Writer) error {
	pw := NewWriter(w)

	// With side-band-64k the status report is multiplexed; without it the
	// report is written raw.
	report := w
	if req.HasCap("side-band-64k") {
		report = SidebandWriter{W: pw, Channel: SidebandData}
	}

	results, err := applyPush(ctx, s, req)

	if req.HasCap("report-status") {
		if werr := writeStatus(report, err, results); werr != nil {
			return werr
		}
		if req.HasCap("side-band-64k") {
			if ferr := pw.WriteFlush(); ferr != nil {
				return ferr
			}
		}
	}
	return err
}

// applyPush stores the objects and then updates the references.
func applyPush(ctx context.Context, s store.Store, req *PushRequest) ([]CommandResult, error) {
	// A delete-only push carries no packfile.
	needsPack := false
	for _, c := range req.Commands {
		if !c.IsDelete() {
			needsPack = true
		}
	}

	if needsPack {
		if err := storePack(ctx, s, req.Pack); err != nil {
			return nil, err
		}
	}

	// Every new tip must actually be in the store now, or the push was
	// incomplete and moving the reference would corrupt the repository.
	results := make([]CommandResult, 0, len(req.Commands))
	var updates []store.RefUpdate
	for _, c := range req.Commands {
		if !c.IsDelete() {
			ok, err := s.Has(ctx, c.New)
			if err != nil {
				return nil, err
			}
			if !ok {
				results = append(results, CommandResult{
					Name: c.Name,
					Err:  fmt.Errorf("object %s is missing from the pack", c.New.Short(8)),
				})
				continue
			}
		}
		updates = append(updates, store.RefUpdate{Name: c.Name, Old: c.Old, New: c.New})
		results = append(results, CommandResult{Name: c.Name})
	}

	if len(updates) == 0 {
		return results, nil
	}

	// One atomic batch. If any comparison loses, nothing moves, and the
	// client is told which reference raced.
	if err := s.UpdateRefs(ctx, updates); err != nil {
		var conflict *store.ErrRefConflict
		if errors.As(err, &conflict) {
			for i := range results {
				if results[i].Name == conflict.Ref {
					results[i].Err = errors.New("stale info: the branch moved under you")
				} else if results[i].Err == nil {
					results[i].Err = errors.New("not attempted: another ref in this push failed")
				}
			}
			return results, nil
		}
		return nil, err
	}
	return results, nil
}

// storePack reads a pushed packfile and writes every object it contains.
//
// A pushed pack may be thin: its deltas can be based on objects the server
// already has but the client did not resend. Those bases are resolved out
// of the store.
func storePack(ctx context.Context, s store.Store, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("receive-pack: read pack: %w", err)
	}
	if len(data) == 0 {
		return nil
	}

	pr, err := pack.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("receive-pack: %w", err)
	}
	if err := pr.VerifyChecksum(); err != nil {
		return fmt.Errorf("receive-pack: %w", err)
	}
	pr.SetExternalResolver(func(id object.ID) (*object.Object, error) {
		return s.Get(ctx, id)
	})

	var objs []*object.Object
	byID := make(map[object.ID]int64, pr.Count())
	if err := pr.Scan(func(e pack.Entry, entryData []byte) error {
		o, err := pr.ObjectAt(e.Offset)
		if err != nil {
			return err
		}
		byID[o.ID()] = e.Offset
		objs = append(objs, o)
		return nil
	}); err != nil {
		return fmt.Errorf("receive-pack: %w", err)
	}

	return s.PutBatch(ctx, objs)
}

// writeStatus sends the report-status response: one line for the push as a
// whole, then one per reference.
func writeStatus(w io.Writer, fatal error, results []CommandResult) error {
	pw := NewWriter(w)

	if fatal != nil {
		if err := pw.WriteLine("unpack " + oneLine(fatal.Error())); err != nil {
			return err
		}
		return pw.WriteFlush()
	}
	if err := pw.WriteLine("unpack ok"); err != nil {
		return err
	}
	for _, r := range results {
		if r.Err != nil {
			err := pw.WriteLine("ng " + r.Name + " " + oneLine(r.Err.Error()))
			if err != nil {
				return err
			}
			continue
		}
		if err := pw.WriteLine("ok " + r.Name); err != nil {
			return err
		}
	}
	return pw.WriteFlush()
}

// oneLine flattens a message, since a status line cannot contain newlines.
func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
}
