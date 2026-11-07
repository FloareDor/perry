package object

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Type is one of git's four object types.
type Type uint8

// The four object types. The numeric values match the type codes used in
// packfile entry headers (1..4), which lets pack code convert without a table.
const (
	TypeInvalid Type = 0
	TypeCommit  Type = 1
	TypeTree    Type = 2
	TypeBlob    Type = 3
	TypeTag     Type = 4
)

// String returns the lowercase type name used in loose object headers.
func (t Type) String() string {
	switch t {
	case TypeCommit:
		return "commit"
	case TypeTree:
		return "tree"
	case TypeBlob:
		return "blob"
	case TypeTag:
		return "tag"
	default:
		return "invalid"
	}
}

// Valid reports whether t is one of the four real object types.
func (t Type) Valid() bool { return t >= TypeCommit && t <= TypeTag }

// ParseType decodes a type name from a loose object header.
func ParseType(s string) (Type, error) {
	switch s {
	case "commit":
		return TypeCommit, nil
	case "tree":
		return TypeTree, nil
	case "blob":
		return TypeBlob, nil
	case "tag":
		return TypeTag, nil
	default:
		return TypeInvalid, fmt.Errorf("object: unknown type %q", s)
	}
}

// Object is a decoded git object: its type and its raw payload, without the
// "<type> <len>\x00" header. Payload is kept as bytes rather than a parsed
// structure so the store never has to re-serialize, which would risk changing
// the bytes and therefore the hash.
type Object struct {
	Type    Type
	Payload []byte
}

// ID returns the object's content address.
func (o *Object) ID() ID { return Hash(o.Type, o.Payload) }

// Size returns the payload length in bytes.
func (o *Object) Size() int64 { return int64(len(o.Payload)) }

// ErrMalformed reports a corrupt or unparseable object encoding.
var ErrMalformed = errors.New("object: malformed encoding")

// maxHeader bounds how far we scan for the NUL that ends a loose object
// header. A real header is under 32 bytes; this guards against a corrupt file
// making us read the whole object looking for a terminator.
const maxHeader = 64

// EncodeLoose writes the zlib-compressed loose object form to w: the header
// "<type> <size>\x00" followed by the payload, all deflated.
func EncodeLoose(w io.Writer, o *Object) error {
	zw := zlib.NewWriter(w)
	if _, err := fmt.Fprintf(zw, "%s %d\x00", o.Type, len(o.Payload)); err != nil {
		return err
	}
	if _, err := zw.Write(o.Payload); err != nil {
		return err
	}
	return zw.Close()
}

// MarshalLoose returns the zlib-compressed loose object bytes.
func MarshalLoose(o *Object) ([]byte, error) {
	var buf bytes.Buffer
	if err := EncodeLoose(&buf, o); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeLoose reads one zlib-compressed loose object from r.
//
// The declared size in the header is trusted only as an expectation: we read
// the payload and then check the actual length matches. A header claiming a
// huge size cannot make us allocate it up front.
func DecodeLoose(r io.Reader) (*Object, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: zlib: %v", ErrMalformed, err)
	}
	defer zr.Close()

	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("%w: inflate: %v", ErrMalformed, err)
	}
	return parseLoosePayload(raw)
}

// UnmarshalLoose decodes loose object bytes.
func UnmarshalLoose(b []byte) (*Object, error) { return DecodeLoose(bytes.NewReader(b)) }

func parseLoosePayload(raw []byte) (*Object, error) {
	limit := len(raw)
	if limit > maxHeader {
		limit = maxHeader
	}
	nul := bytes.IndexByte(raw[:limit], 0)
	if nul < 0 {
		return nil, fmt.Errorf("%w: no header terminator in first %d bytes", ErrMalformed, limit)
	}

	sp := bytes.IndexByte(raw[:nul], ' ')
	if sp < 0 {
		return nil, fmt.Errorf("%w: header %q has no space", ErrMalformed, raw[:nul])
	}

	typ, err := ParseType(string(raw[:sp]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	size, err := strconv.ParseInt(string(raw[sp+1:nul]), 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("%w: bad size %q", ErrMalformed, raw[sp+1:nul])
	}

	payload := raw[nul+1:]
	if int64(len(payload)) != size {
		return nil, fmt.Errorf("%w: header says %d bytes, payload has %d", ErrMalformed, size, len(payload))
	}
	return &Object{Type: typ, Payload: payload}, nil
}
