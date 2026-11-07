// Package object implements git's object model: the four object types, their
// on-disk loose encoding, and SHA-1 content addressing.
package object

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
)

// IDSize is the length in bytes of a SHA-1 object name.
const IDSize = 20

// ID is a SHA-1 object name. Git identifies every object by the SHA-1 of its
// uncompressed header plus payload, so IDs are content addresses: equal content
// always yields an equal ID.
type ID [IDSize]byte

// ZeroID is the all-zero object name. Git uses it on the wire to mean "no
// object": a ref create has a zero old value, a ref delete has a zero new one.
var ZeroID ID

// IsZero reports whether id is the all-zero object name.
func (id ID) IsZero() bool { return id == ZeroID }

// String returns the 40-character lowercase hex form.
func (id ID) String() string { return hex.EncodeToString(id[:]) }

// Short returns the first n hex characters, clamped to the full length.
func (id ID) Short(n int) string {
	s := id.String()
	if n > len(s) {
		n = len(s)
	}
	return s[:n]
}

// Bytes returns a copy of the raw 20 bytes.
func (id ID) Bytes() []byte {
	b := make([]byte, IDSize)
	copy(b, id[:])
	return b
}

// Compare orders two IDs bytewise. It returns a negative number if id sorts
// before other, zero if they are equal, and a positive number otherwise.
func (id ID) Compare(other ID) int {
	for i := 0; i < IDSize; i++ {
		if id[i] != other[i] {
			return int(id[i]) - int(other[i])
		}
	}
	return 0
}

// ErrBadID reports a malformed object name.
var ErrBadID = errors.New("object: malformed object id")

// ParseID decodes a 40-character hex object name.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != IDSize*2 {
		return id, fmt.Errorf("%w: want %d hex chars, got %d", ErrBadID, IDSize*2, len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, fmt.Errorf("%w: %v", ErrBadID, err)
	}
	copy(id[:], b)
	return id, nil
}

// MustParseID is ParseID but panics on error. Use only for constants in tests.
func MustParseID(s string) ID {
	id, err := ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// IDFromBytes copies raw bytes into an ID.
func IDFromBytes(b []byte) (ID, error) {
	var id ID
	if len(b) != IDSize {
		return id, fmt.Errorf("%w: want %d bytes, got %d", ErrBadID, IDSize, len(b))
	}
	copy(id[:], b)
	return id, nil
}

// Hash computes the object name for a type and payload. This is the definition
// of git's content addressing: SHA-1 over "<type> <len>\x00" followed by the
// payload. The same bytes always produce the same name, on any machine.
func Hash(t Type, payload []byte) ID {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", t, len(payload))
	h.Write(payload)
	var id ID
	copy(id[:], h.Sum(nil))
	return id
}
