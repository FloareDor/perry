package object

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
)

// Mode is a tree entry's file mode. Git stores a small fixed set of modes, not
// arbitrary POSIX permissions.
type Mode uint32

// The modes git actually writes.
const (
	ModeDir     Mode = 0o040000 // a subtree
	ModeRegular Mode = 0o100644 // a non-executable file
	ModeExec    Mode = 0o100755 // an executable file
	ModeSymlink Mode = 0o120000 // a symlink; the blob holds the target path
	ModeSubmod  Mode = 0o160000 // a gitlink: a commit id from another repo
)

// String renders the mode the way a tree entry stores it: octal with no
// leading zero, so a directory is "40000" and a file is "100644".
func (m Mode) String() string { return strconv.FormatUint(uint64(m), 8) }

// IsDir reports whether the entry is a subtree.
func (m Mode) IsDir() bool { return m == ModeDir }

// IsSubmodule reports whether the entry points at a commit in another repo.
// Such a commit is not present in this repository, so a tree walk must not
// try to resolve it.
func (m Mode) IsSubmodule() bool { return m == ModeSubmod }

// Valid reports whether m is one of the modes git writes.
func (m Mode) Valid() bool {
	switch m {
	case ModeDir, ModeRegular, ModeExec, ModeSymlink, ModeSubmod:
		return true
	}
	return false
}

// TreeEntry is one row of a tree: a mode, a path component, and the object it
// names. Name is a single component, never a path with slashes.
type TreeEntry struct {
	Mode Mode
	Name string
	ID   ID
}

// Tree is a directory listing.
type Tree struct {
	Entries []TreeEntry
}

// DecodeTree parses a tree payload.
//
// The wire form is a flat concatenation with no count and no separators:
// "<octal mode> <name>\x00<20 raw bytes>" repeated to the end.
func DecodeTree(payload []byte) (*Tree, error) {
	t := &Tree{}
	for len(payload) > 0 {
		sp := bytes.IndexByte(payload, ' ')
		if sp < 0 {
			return nil, fmt.Errorf("%w: tree entry has no mode separator", ErrMalformed)
		}
		mode64, err := strconv.ParseUint(string(payload[:sp]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: bad tree mode %q", ErrMalformed, payload[:sp])
		}
		payload = payload[sp+1:]

		nul := bytes.IndexByte(payload, 0)
		if nul < 0 {
			return nil, fmt.Errorf("%w: tree entry name is unterminated", ErrMalformed)
		}
		name := string(payload[:nul])
		payload = payload[nul+1:]

		if len(payload) < IDSize {
			return nil, fmt.Errorf("%w: tree entry %q is truncated before its id", ErrMalformed, name)
		}
		var id ID
		copy(id[:], payload[:IDSize])
		payload = payload[IDSize:]

		t.Entries = append(t.Entries, TreeEntry{Mode: Mode(mode64), Name: name, ID: id})
	}
	return t, nil
}

// Encode serializes the tree, sorting entries into git's canonical order
// first. Git sorts bytewise by name, except that a directory sorts as though
// its name ended in "/". Getting this wrong changes the tree's hash, so the
// sort is part of the format rather than a convenience.
func (t *Tree) Encode() []byte {
	entries := make([]TreeEntry, len(t.Entries))
	copy(entries, t.Entries)
	sort.Slice(entries, func(i, j int) bool {
		return treeSortKey(entries[i]) < treeSortKey(entries[j])
	})

	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.Mode.String())
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte(0)
		buf.Write(e.ID[:])
	}
	return buf.Bytes()
}

func treeSortKey(e TreeEntry) string {
	if e.Mode.IsDir() {
		return e.Name + "/"
	}
	return e.Name
}

// Object returns the tree as a generic object ready to store.
func (t *Tree) Object() *Object { return &Object{Type: TypeTree, Payload: t.Encode()} }

// Lookup finds an entry by exact name.
func (t *Tree) Lookup(name string) (TreeEntry, bool) {
	for _, e := range t.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return TreeEntry{}, false
}
