package api

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/store"
)

// Edit replaces one exact run of text inside a file.
//
// This exists because whole-file writes are expensive in the only currency
// that turned out to matter. Measured against git on a 733-line file, a model
// asked to re-emit the complete contents spent 15,040 output tokens and 75
// seconds; the same model using a search-and-replace editor spent 585 tokens
// and 16. The network cost perry saves is real and was never the bottleneck.
//
// The format is search-and-replace rather than a unified diff on purpose. A
// diff carries line numbers and context the model has to get right against a
// file it is reconstructing from memory, and when it gets them wrong the
// patch can still apply, in the wrong place. An exact string either occurs
// once or it does not: ambiguity is rejected here rather than resolved by
// guessing.
type Edit struct {
	// Old is the exact text to find. It must occur exactly once in the
	// file unless All is set.
	Old string `json:"old"`
	// New is what replaces it. Empty deletes the matched text.
	New string `json:"new"`
	// All replaces every occurrence instead of requiring a unique one.
	All bool `json:"all,omitempty"`
}

// applyEdits runs a change's edits against the file's current contents.
//
// Each edit sees the result of the ones before it, which is what lets a model
// make several independent changes to one file in a single commit.
func applyEdits(filePath string, content []byte, edits []Edit) ([]byte, error) {
	s := string(content)
	for i, e := range edits {
		if e.Old == "" {
			return nil, fmt.Errorf("api: %s edit %d has no old text; "+
				"send content instead to replace the whole file", filePath, i+1)
		}
		n := strings.Count(s, e.Old)
		switch {
		case n == 0:
			return nil, fmt.Errorf("api: %s edit %d did not match%s",
				filePath, i+1, nearMissHint(s, e.Old))
		case n > 1 && !e.All:
			return nil, fmt.Errorf("api: %s edit %d matched %d times; "+
				"include more surrounding lines to make it unique, or set all",
				filePath, i+1, n)
		case e.All:
			s = strings.ReplaceAll(s, e.Old, e.New)
		default:
			s = strings.Replace(s, e.Old, e.New, 1)
		}
	}
	return []byte(s), nil
}

// nearMissHint explains the two ways an edit misses that a model cannot see
// by re-reading its own output. Both are invisible in a terminal, so without
// this the model's only recourse is to re-read the file and guess again.
func nearMissHint(content, old string) string {
	normal := strings.ReplaceAll(content, "\r\n", "\n")
	if strings.Contains(normal, strings.ReplaceAll(old, "\r\n", "\n")) {
		return " (it matches apart from line endings; the file uses CRLF)"
	}
	if strings.Contains(squeeze(normal), squeeze(old)) {
		return " (it matches apart from whitespace; copy the indentation exactly)"
	}
	return "; the old text must appear in the file exactly, character for character"
}

func squeeze(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// blobAt reads one file out of an already-resolved tree.
//
// Commit has the base tree in hand and must not resolve the branch a second
// time: between the two reads another agent could move it, and the edits
// would then apply to contents that are not the ones being committed onto.
// The mode comes back with the contents because an edit must not change it.
// A commit that rewrote one line of a shell script and silently dropped its
// executable bit would be a quiet, committed regression.
func blobAt(ctx context.Context, s store.Store, treeID object.ID, filePath string) ([]byte, object.Mode, error) {
	if treeID.IsZero() {
		return nil, 0, fmt.Errorf("%w: %s (the branch has no commits yet)", store.ErrNotFound, filePath)
	}
	dir, name := path.Split(filePath)
	dirTree, err := descend(ctx, s, treeID, strings.TrimSuffix(dir, "/"))
	if err != nil {
		return nil, 0, err
	}
	tree, err := readTree(ctx, s, dirTree)
	if err != nil {
		return nil, 0, err
	}
	entry, ok := tree.Lookup(name)
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", store.ErrNotFound, filePath)
	}
	if entry.Mode.IsDir() {
		return nil, 0, fmt.Errorf("api: %s is a directory", filePath)
	}
	obj, err := s.Get(ctx, entry.ID)
	if err != nil {
		return nil, 0, err
	}
	return obj.Payload, entry.Mode, nil
}

// ReadFileRange returns a line range of a file, and the file's total length
// in lines.
//
// Reading a whole file to change its last line is how an agent spends its
// context, and context is what it pays for. An agent with a shell reaches
// for tail; one with only these tools had no equivalent, so it took all
// 733 lines of a file to append one. Lines are 1-based and inclusive, which
// is how every editor and every error message counts them. A zero end means
// "to the end of the file", and a negative start counts back from the end,
// so -20 is the last twenty lines.
func ReadFileRange(ctx context.Context, s store.Store, rev, filePath string, start, end int) ([]byte, int, error) {
	data, err := ReadFile(ctx, s, rev, filePath)
	if err != nil {
		return nil, 0, err
	}
	lines := strings.Split(string(data), "\n")
	// A file ending in a newline splits into a trailing empty element that
	// is not a line. Dropping it keeps the count honest and keeps a
	// tail from being one blank line short.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if start == 0 && end == 0 {
		return data, total, nil
	}

	if start < 0 {
		start = total + start + 1
	}
	if start < 1 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	if start > total {
		return nil, total, fmt.Errorf("api: %s has %d lines; line %d is past the end", filePath, total, start)
	}
	if start > end {
		return nil, total, fmt.Errorf("api: %s: line %d is after line %d", filePath, start, end)
	}
	return []byte(strings.Join(lines[start-1:end], "\n") + "\n"), total, nil
}
