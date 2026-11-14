package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/pack"
	"github.com/FloareDor/perry/internal/store"
)

// Agent identifies this server in capability advertisements.
const Agent = "perry/0.1"

// Capabilities advertised for upload-pack.
//
// Kept deliberately small. Every capability here is one the server actually
// honours, because a client that sees a capability advertised will use it.
const uploadPackCaps = "side-band-64k ofs-delta no-progress agent=" + Agent

// zeroID is the all-zero object name, used to advertise an empty repository.
const zeroIDHex = "0000000000000000000000000000000000000000"

// capabilitiesRef is the placeholder name git uses when a repository has no
// references at all, so that an empty repo can still carry a capability
// list.
const capabilitiesRef = "capabilities^{}"

// AdvertiseRefs writes the reference advertisement for a service.
//
// Over smart HTTP the advertisement opens with a service line and a flush,
// which lets a client tell a real smart server from a dumb file server
// before it commits to the rest of the exchange.
func AdvertiseRefs(w io.Writer, service string, refs []store.Ref, headTarget string) error {
	pw := NewWriter(w)

	if err := pw.WriteLine("# service=" + service); err != nil {
		return err
	}
	if err := pw.WriteFlush(); err != nil {
		return err
	}

	caps := uploadPackCaps
	if service == "git-receive-pack" {
		caps = receivePackCaps
	}

	// HEAD is advertised first when it resolves, because the client uses it
	// to decide which branch to check out after a clone.
	ordered := orderRefs(refs, headTarget)

	if len(ordered) == 0 {
		// An empty repository still has to advertise its capabilities, and
		// the placeholder ref is the only place to hang them.
		if err := pw.WriteLine(zeroIDHex + " " + capabilitiesRef + "\x00" + caps); err != nil {
			return err
		}
		return pw.WriteFlush()
	}

	for i, r := range ordered {
		line := r.Target.String() + " " + r.Name
		if i == 0 {
			// Capabilities ride on the first ref line, after a NUL. Only
			// the first one carries them.
			line += "\x00" + caps
		}
		if err := pw.WriteLine(line); err != nil {
			return err
		}
	}
	return pw.WriteFlush()
}

// orderRefs puts HEAD first, then the remaining refs sorted by name.
func orderRefs(refs []store.Ref, headTarget string) []store.Ref {
	sorted := make([]store.Ref, len(refs))
	copy(sorted, refs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var head *store.Ref
	for i := range sorted {
		if sorted[i].Name == headTarget {
			head = &sorted[i]
			break
		}
	}
	if head == nil {
		return sorted
	}

	out := make([]store.Ref, 0, len(sorted)+1)
	out = append(out, store.Ref{Name: "HEAD", Target: head.Target})
	out = append(out, sorted...)
	return out
}

// UploadRequest is a parsed upload-pack request.
type UploadRequest struct {
	Wants        []object.ID
	Haves        []object.ID
	Done         bool
	Capabilities map[string]string
}

// HasCap reports whether the client asked for a capability.
func (r *UploadRequest) HasCap(name string) bool {
	_, ok := r.Capabilities[name]
	return ok
}

// ParseUploadRequest reads the want/have negotiation from a request body.
//
// The shape is: one or more "want" lines, the first carrying the client's
// capabilities, then a flush, then zero or more "have" lines, then "done".
// Over HTTP each round of negotiation is its own request, so a body may end
// after the wants with no haves at all, which is what a clone looks like.
func ParseUploadRequest(r io.Reader) (*UploadRequest, error) {
	req := &UploadRequest{Capabilities: map[string]string{}}
	pr := NewReader(r)

	for {
		kind, payload, err := pr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if kind != KindData {
			// A flush ends the want section. Haves may follow in the same
			// body, so keep reading rather than stopping here.
			continue
		}

		line := strings.TrimSuffix(string(payload), "\n")
		switch {
		case strings.HasPrefix(line, "want "):
			rest := line[len("want "):]
			// The first want carries the capability list after a space.
			if sp := strings.IndexByte(rest, ' '); sp >= 0 {
				parseCaps(rest[sp+1:], req.Capabilities)
				rest = rest[:sp]
			}
			id, err := object.ParseID(rest)
			if err != nil {
				return nil, fmt.Errorf("upload-pack: bad want: %w", err)
			}
			req.Wants = append(req.Wants, id)

		case strings.HasPrefix(line, "have "):
			id, err := object.ParseID(line[len("have "):])
			if err != nil {
				return nil, fmt.Errorf("upload-pack: bad have: %w", err)
			}
			req.Haves = append(req.Haves, id)

		case line == "done":
			req.Done = true

		case strings.HasPrefix(line, "deepen"), strings.HasPrefix(line, "shallow "),
			strings.HasPrefix(line, "filter "):
			// Shallow and partial clone are not supported. Ignoring these
			// is safe: the client gets a complete history instead of the
			// truncated one it asked for, which is correct, just larger.

		case line == "":
			// Some clients send an empty packet before "done".

		default:
			return nil, fmt.Errorf("upload-pack: unexpected line %q", line)
		}
	}

	if len(req.Wants) == 0 {
		return nil, errors.New("upload-pack: request has no want lines")
	}
	return req, nil
}

func parseCaps(s string, into map[string]string) {
	for _, c := range strings.Fields(s) {
		if eq := strings.IndexByte(c, '='); eq >= 0 {
			into[c[:eq]] = c[eq+1:]
		} else {
			into[c] = ""
		}
	}
}

// UploadPack serves a fetch: it works out which objects the client is
// missing, packs them, and writes the response.
func UploadPack(ctx context.Context, s store.Store, req *UploadRequest, w io.Writer) error {
	pw := NewWriter(w)

	// Refuse to serve an object no advertised reference leads to. Without
	// this check a client that learns an object name out of band could read
	// content from a branch it was never shown, for example one that was
	// force-pushed away but not yet garbage collected.
	refs, err := s.ListRefs(ctx, "refs/")
	if err != nil {
		return err
	}
	roots := make([]object.ID, 0, len(refs))
	for _, r := range refs {
		roots = append(roots, r.Target)
	}
	advertised := make(map[object.ID]bool, len(roots))
	for _, id := range roots {
		advertised[id] = true
	}
	for _, wantID := range req.Wants {
		if advertised[wantID] {
			continue
		}
		ok, err := Reachable(ctx, s, roots, wantID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("upload-pack: %s is not reachable from any advertised ref", wantID)
		}
	}

	objs, err := Walk(ctx, s, req.Wants, req.Haves)
	if err != nil {
		return err
	}

	// Acknowledge the negotiation. With no common history there is nothing
	// to acknowledge, so the answer is NAK and the whole history follows.
	if err := pw.WriteLine("NAK"); err != nil {
		return err
	}

	// side-band-64k multiplexes pack data, progress and errors onto one
	// connection. Without it the pack is written raw and an error mid-pack
	// can only be signalled by closing the connection.
	var dest io.Writer = w
	useSideband := req.HasCap("side-band-64k")
	if useSideband {
		dest = SidebandWriter{W: pw, Channel: SidebandData}
	}

	if _, err := pack.WriteAll(dest, objs); err != nil {
		if useSideband {
			_ = pw.WriteError("perry: " + err.Error())
		}
		return fmt.Errorf("upload-pack: write pack: %w", err)
	}

	if useSideband {
		if err := pw.WriteFlush(); err != nil {
			return err
		}
	}
	return pw.Err()
}
