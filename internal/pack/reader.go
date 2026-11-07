package pack

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/perry-dev/perry/internal/object"
)

// Packfile layout constants.
const (
	magic       = "PACK"
	version     = 2
	headerSize  = 12 // magic + version + object count
	trailerSize = object.IDSize
)

// Entry type codes as they appear in a pack entry header. The first four
// match object.Type exactly; 5 is unused and 6 and 7 are the two delta forms.
const (
	typeOfsDelta = 6
	typeRefDelta = 7
)

// Header describes a packfile's fixed prologue.
type Header struct {
	Version uint32
	Count   uint32
}

// Entry is one object slot in a packfile, before delta resolution.
type Entry struct {
	Offset     int64     // byte offset of the entry header within the pack
	Type       uint8     // raw pack type code, including the two delta codes
	Size       int64     // inflated size of this entry's own stream
	BaseOffset int64     // for an offset delta: the absolute offset of the base
	BaseID     object.ID // for a reference delta: the base's object name
	DataOffset int64     // byte offset of the deflate stream
}

// IsDelta reports whether the entry stores a delta rather than a whole object.
func (e Entry) IsDelta() bool { return e.Type == typeOfsDelta || e.Type == typeRefDelta }

// ExternalResolver supplies objects that a thin pack references but does not
// contain. A fetch may send a pack whose deltas are based on objects the
// client already has, so resolution has to be able to reach outside the file.
type ExternalResolver func(object.ID) (*object.Object, error)

// Reader reads objects out of a packfile.
//
// It is safe for concurrent use. The delta cache is the only shared mutable
// state and it is guarded by its own mutex, so many agents can read from one
// pack at the same time.
type Reader struct {
	r    io.ReaderAt
	size int64
	hdr  Header

	external ExternalResolver

	mu         sync.Mutex
	cache      map[int64]*object.Object // resolved objects, keyed by pack offset
	offsetByID map[object.ID]int64      // populated by BuildOffsetIndex, for ref deltas
}

// NewReader parses a packfile's header and returns a reader over it.
func NewReader(r io.ReaderAt, size int64) (*Reader, error) {
	if size < headerSize+trailerSize {
		return nil, fmt.Errorf("pack: file is %d bytes, too short to be a pack", size)
	}

	var hdr [headerSize]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return nil, fmt.Errorf("pack: read header: %w", err)
	}
	if string(hdr[0:4]) != magic {
		return nil, fmt.Errorf("pack: bad magic %q", hdr[0:4])
	}
	v := binary.BigEndian.Uint32(hdr[4:8])
	if v != version {
		return nil, fmt.Errorf("pack: unsupported version %d", v)
	}

	return &Reader{
		r:     r,
		size:  size,
		hdr:   Header{Version: v, Count: binary.BigEndian.Uint32(hdr[8:12])},
		cache: make(map[int64]*object.Object),
	}, nil
}

// SetExternalResolver installs a lookup for bases a thin pack omits.
func (p *Reader) SetExternalResolver(f ExternalResolver) { p.external = f }

// Count returns the number of objects the header declares.
func (p *Reader) Count() uint32 { return p.hdr.Count }

// Size returns the packfile length in bytes.
func (p *Reader) Size() int64 { return p.size }

// readEntryHeader decodes the variable-length header at off.
//
// The first byte carries a continuation bit, a three-bit type, and the low
// four bits of the size. Each continuation byte adds seven more size bits of
// increasing significance. Delta entries then carry their base reference
// before the deflate stream begins.
func (p *Reader) readEntryHeader(off int64) (Entry, error) {
	var e Entry
	e.Offset = off

	buf := make([]byte, 32)
	n, err := p.r.ReadAt(buf, off)
	if err != nil && n == 0 {
		return e, fmt.Errorf("pack: read entry header at %d: %w", off, err)
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return e, fmt.Errorf("pack: no entry header at %d", off)
	}

	c := buf[0]
	e.Type = (c >> 4) & 0x07
	size := int64(c & 0x0f)
	shift := uint(4)
	i := 1
	for c&0x80 != 0 {
		if i >= len(buf) {
			return e, fmt.Errorf("pack: entry header at %d is truncated", off)
		}
		if shift >= 63 {
			return e, fmt.Errorf("pack: entry size at %d overflows", off)
		}
		c = buf[i]
		size |= int64(c&0x7f) << shift
		shift += 7
		i++
	}
	e.Size = size

	switch e.Type {
	case typeOfsDelta:
		// The base is given as a positive distance backwards from this
		// entry, in a varint whose continuation adds one before shifting.
		// That encoding has no redundant forms, so every distance has
		// exactly one representation.
		if i >= len(buf) {
			return e, fmt.Errorf("pack: offset delta at %d is truncated", off)
		}
		c = buf[i]
		i++
		dist := int64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= len(buf) {
				return e, fmt.Errorf("pack: offset delta distance at %d is truncated", off)
			}
			c = buf[i]
			i++
			dist = ((dist + 1) << 7) | int64(c&0x7f)
		}
		e.BaseOffset = off - dist
		if e.BaseOffset < headerSize || e.BaseOffset >= off {
			return e, fmt.Errorf("pack: offset delta at %d points outside the pack (base %d)", off, e.BaseOffset)
		}

	case typeRefDelta:
		if i+object.IDSize > len(buf) {
			return e, fmt.Errorf("pack: reference delta at %d is truncated", off)
		}
		copy(e.BaseID[:], buf[i:i+object.IDSize])
		i += object.IDSize

	default:
		if e.Type < 1 || e.Type > 4 {
			return e, fmt.Errorf("pack: entry at %d has reserved type %d", off, e.Type)
		}
	}

	e.DataOffset = off + int64(i)
	return e, nil
}

// inflateAt decompresses the deflate stream starting at off and reports how
// many compressed bytes it consumed, which is how the sequential scanner
// finds the next entry.
func (p *Reader) inflateAt(off int64, expect int64) ([]byte, int64, error) {
	sr := io.NewSectionReader(p.r, off, p.size-off)
	cr := &countingReader{r: sr}

	zr, err := zlib.NewReader(cr)
	if err != nil {
		return nil, 0, fmt.Errorf("pack: zlib at %d: %w", off, err)
	}
	defer zr.Close()

	// The entry header already declared the inflated size, so the buffer is
	// allocated once rather than grown. A stream that disagrees is corrupt.
	out := make([]byte, expect)
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, 0, fmt.Errorf("pack: inflate at %d: %w", off, err)
	}

	// Drain the rest of the deflate stream so the consumed count covers the
	// whole member, including its trailing adler32 checksum.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return nil, 0, fmt.Errorf("pack: drain at %d: %w", off, err)
	}
	return out, cr.n, nil
}

// ObjectAt resolves the object stored at a pack offset, following any delta
// chain to a whole object and applying the deltas on the way back.
func (p *Reader) ObjectAt(off int64) (*object.Object, error) {
	return p.objectAt(off, 0)
}

func (p *Reader) objectAt(off int64, depth int) (*object.Object, error) {
	if depth > maxDeltaDepth {
		return nil, fmt.Errorf("pack: delta chain at %d exceeds depth %d (corrupt or cyclic)", off, maxDeltaDepth)
	}

	p.mu.Lock()
	if o, ok := p.cache[off]; ok {
		p.mu.Unlock()
		return o, nil
	}
	p.mu.Unlock()

	e, err := p.readEntryHeader(off)
	if err != nil {
		return nil, err
	}
	data, _, err := p.inflateAt(e.DataOffset, e.Size)
	if err != nil {
		return nil, err
	}

	var result *object.Object
	switch e.Type {
	case typeOfsDelta:
		base, err := p.objectAt(e.BaseOffset, depth+1)
		if err != nil {
			return nil, fmt.Errorf("pack: base of delta at %d: %w", off, err)
		}
		payload, err := ApplyDelta(base.Payload, data)
		if err != nil {
			return nil, fmt.Errorf("pack: apply delta at %d: %w", off, err)
		}
		// A delta never changes the type; it inherits its base's.
		result = &object.Object{Type: base.Type, Payload: payload}

	case typeRefDelta:
		base, err := p.resolveByID(e.BaseID, depth+1)
		if err != nil {
			return nil, fmt.Errorf("pack: base %s of delta at %d: %w", e.BaseID, off, err)
		}
		payload, err := ApplyDelta(base.Payload, data)
		if err != nil {
			return nil, fmt.Errorf("pack: apply delta at %d: %w", off, err)
		}
		result = &object.Object{Type: base.Type, Payload: payload}

	default:
		result = &object.Object{Type: object.Type(e.Type), Payload: data}
	}

	p.mu.Lock()
	p.cache[off] = result
	p.mu.Unlock()
	return result, nil
}

// resolveByID finds a reference-delta base, first inside this pack and then
// through the external resolver if the pack is thin.
func (p *Reader) resolveByID(id object.ID, depth int) (*object.Object, error) {
	p.mu.Lock()
	byID := p.offsetByID
	p.mu.Unlock()

	if byID != nil {
		if off, ok := byID[id]; ok {
			return p.objectAt(off, depth)
		}
	}
	if p.external != nil {
		return p.external(id)
	}
	return nil, fmt.Errorf("pack: base %s is not in this pack and no external resolver is set", id)
}

// Scan walks every entry in the pack in offset order and calls fn.
//
// Entries are laid out back to back with no table of contents, so the only
// way to find entry k+1 is to inflate entry k and see where its stream ends.
// This is why building an index is a full pass over the file.
func (p *Reader) Scan(fn func(Entry) error) error {
	off := int64(headerSize)
	for i := uint32(0); i < p.hdr.Count; i++ {
		e, err := p.readEntryHeader(off)
		if err != nil {
			return fmt.Errorf("pack: entry %d of %d: %w", i, p.hdr.Count, err)
		}
		_, consumed, err := p.inflateAt(e.DataOffset, e.Size)
		if err != nil {
			return fmt.Errorf("pack: entry %d of %d: %w", i, p.hdr.Count, err)
		}
		if err := fn(e); err != nil {
			return err
		}
		off = e.DataOffset + consumed
	}

	// Everything after the final entry must be exactly the trailer.
	if remaining := p.size - off; remaining != trailerSize {
		return fmt.Errorf("pack: %d bytes remain after the last entry, want exactly %d (the trailer)",
			remaining, trailerSize)
	}
	return nil
}

// BuildOffsetIndex scans the pack once and records every object's name and
// offset, which is what reference deltas and the v2 index both need.
//
// Resolving a name requires the object's content, and a delta's content
// requires its base, so the scan walks entries in offset order: a delta's
// base always appears earlier in the file, so it is always already known.
func (p *Reader) BuildOffsetIndex() (map[object.ID]int64, error) {
	byID := make(map[object.ID]int64, p.hdr.Count)

	// Publish the map as it fills so that a reference delta encountered
	// mid-scan can find a base that was already indexed.
	p.mu.Lock()
	p.offsetByID = byID
	p.mu.Unlock()

	err := p.Scan(func(e Entry) error {
		o, err := p.ObjectAt(e.Offset)
		if err != nil {
			return err
		}
		byID[o.ID()] = e.Offset
		return nil
	})
	if err != nil {
		return nil, err
	}
	return byID, nil
}

// VerifyChecksum recomputes the SHA-1 over the pack body and compares it with
// the 20-byte trailer.
func (p *Reader) VerifyChecksum() error {
	h := sha1.New()
	if _, err := io.Copy(h, io.NewSectionReader(p.r, 0, p.size-trailerSize)); err != nil {
		return fmt.Errorf("pack: checksum read: %w", err)
	}
	want := make([]byte, trailerSize)
	if _, err := p.r.ReadAt(want, p.size-trailerSize); err != nil {
		return fmt.Errorf("pack: read trailer: %w", err)
	}
	if got := h.Sum(nil); !bytes.Equal(got, want) {
		return fmt.Errorf("pack: checksum mismatch: computed %x, trailer says %x", got, want)
	}
	return nil
}

// countingReader reports exactly how many bytes a decompressor consumed.
//
// It implements io.ByteReader deliberately. compress/flate wraps a plain
// io.Reader in a bufio.Reader and then reads ahead, so the underlying count
// would overshoot the end of the deflate stream by however much happened to
// be buffered. Pack entries are stored back to back with no separator, so an
// overshooting count puts the next entry at the wrong offset and the whole
// scan derails. Offering ByteReader makes flate read through this type
// directly, and the count becomes exact.
type countingReader struct {
	r   io.Reader
	n   int64
	one [1]byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	n, err := c.r.Read(c.one[:])
	c.n += int64(n)
	if n == 1 {
		return c.one[0], nil
	}
	if err == nil {
		err = io.ErrNoProgress
	}
	return 0, err
}
