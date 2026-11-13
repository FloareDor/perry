package pack

import (
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash"
	"io"

	"github.com/perry-dev/perry/internal/object"
)

// Default delta search parameters. The window is how many recently written
// objects a new object may be deltified against; the depth is how long a
// chain of deltas may grow before an object is written whole.
//
// A bigger window finds better bases and costs more time; a deeper chain
// saves more bytes and costs more work to read back, since every link has to
// be applied. Git's defaults are 10 and 50, and perry keeps them.
const (
	DefaultWindow = 10
	DefaultDepth  = 50
)

// DefaultDepthPenalty is how much base selection discounts a candidate that
// is already deep in a delta chain. At 0 the writer picks whichever base
// yields the smallest delta, which is what it used to do; at 1 a base sitting
// at the depth limit must halve the delta to be worth taking.
//
// 0.5 was measured across windows 10 to 250 on two corpora. It is the
// smallest value that makes pack size fall monotonically as the window grows,
// and it is no worse than 0 at the default window on either corpus. Larger
// values keep shrinking the widest packs but start costing bytes at window
// 10, which is the one that ships.
//
// See (*Writer).score for why this is not zero.
const DefaultDepthPenalty = 0.5

// Writer builds a packfile.
//
// Objects are added one at a time and written immediately, so memory stays
// proportional to the delta window rather than to the pack. A clone of a
// large repository streams out without ever holding the whole pack.
type Writer struct {
	w       io.Writer // tees to raw and hash
	raw     io.Writer // the destination alone, for the unhashed trailer
	hash    hash.Hash
	count   uint32
	written uint32
	offset  int64

	window int
	depth  int

	// depthPenalty biases base selection against candidates that already sit
	// deep in a delta chain. See DefaultDepthPenalty.
	depthPenalty float64

	// recent is the delta window: the last few objects written, kept
	// uncompressed so a new object can be compared against them.
	recent []windowEntry
}

type windowEntry struct {
	id      object.ID
	typ     object.Type
	payload []byte
	offset  int64
	depth   int
}

// NewWriter starts a packfile that will contain exactly count objects.
//
// The count goes in the header, which is written before any object is known,
// so it has to be declared up front. Writing a different number of objects
// produces a pack that git will reject.
func NewWriter(w io.Writer, count uint32) (*Writer, error) {
	pw := &Writer{
		hash:   sha1.New(),
		count:  count,
		window: DefaultWindow,
		depth:  DefaultDepth,

		depthPenalty: DefaultDepthPenalty,
	}
	pw.raw = w
	pw.w = io.MultiWriter(w, pw.hash)

	var hdr [headerSize]byte
	copy(hdr[0:4], magic)
	binary.BigEndian.PutUint32(hdr[4:8], version)
	binary.BigEndian.PutUint32(hdr[8:12], count)
	if _, err := pw.w.Write(hdr[:]); err != nil {
		return nil, fmt.Errorf("pack: write header: %w", err)
	}
	pw.offset = headerSize
	return pw, nil
}

// SetWindow sets how many recent objects are considered as delta bases.
// A window of zero disables deltification and writes every object whole.
func (pw *Writer) SetWindow(n int) { pw.window = n }

// SetDepth sets the longest delta chain the writer will build.
//
// It is clamped to MaxDepth. Without the clamp a caller could ask for chains
// longer than the reader will follow, and the resulting pack would be
// unreadable: a cold read of its deepest object fails with an error that
// claims the pack is corrupt or cyclic, when it is neither. Writing a pack
// this package cannot read back is never what the caller meant.
func (pw *Writer) SetDepth(n int) {
	if n < 0 {
		n = 0
	}
	if n > MaxDepth {
		n = MaxDepth
	}
	pw.depth = n
}

// WriteObject appends one object, deltifying it against the window when that
// is smaller than storing it whole.
func (pw *Writer) WriteObject(o *object.Object) error {
	if pw.written >= pw.count {
		return fmt.Errorf("pack: header declared %d objects, tried to write %d", pw.count, pw.written+1)
	}

	base, delta := pw.findBase(o)

	var err error
	start := pw.offset
	if base != nil {
		err = pw.writeDelta(start, base, delta)
	} else {
		err = pw.writeWhole(o)
	}
	if err != nil {
		return err
	}

	// Record the object in the window in its reconstructed form, so later
	// objects delta against real content rather than against a delta.
	d := 0
	if base != nil {
		d = base.depth + 1
	}
	pw.pushWindow(windowEntry{
		id:      o.ID(),
		typ:     o.Type,
		payload: o.Payload,
		offset:  start,
		depth:   d,
	})
	pw.written++
	return nil
}

// score rates a candidate base: the delta it produces, inflated by how deep
// the chain behind it already runs.
//
// Choosing purely on delta size is locally optimal and globally wrong. The
// smallest delta often points at a base that is already deep in a chain, and
// taking it pushes that chain one link closer to the limit. The cost lands on
// later objects: once a base sits at the limit it is refused for everything
// after it, and those objects are written whole. On a corpus with long file
// histories that is enough to make a wider delta window produce a *bigger*
// pack, because the extra candidates a wide window finds are mostly deep ones.
//
// The penalty rises linearly with depth, so a base at the limit has to beat a
// fresh one by DefaultDepthPenalty to win.
func (pw *Writer) score(deltaLen, baseDepth int) float64 {
	if pw.depth <= 0 || pw.depthPenalty == 0 {
		return float64(deltaLen)
	}
	return float64(deltaLen) * (1 + pw.depthPenalty*float64(baseDepth)/float64(pw.depth))
}

// findBase picks the window entry with the best score, or nil if no candidate
// beats writing the object whole.
func (pw *Writer) findBase(o *object.Object) (*windowEntry, []byte) {
	if pw.window == 0 || len(o.Payload) == 0 {
		return nil, nil
	}

	var bestEntry *windowEntry
	var bestDelta []byte
	bestScore := 0.0

	for i := range pw.recent {
		cand := &pw.recent[i]
		// A delta inherits its base's type, so the types must match.
		if cand.typ != o.Type {
			continue
		}
		// Refuse to extend a chain past the depth limit, or the reader would
		// have to apply an unbounded number of deltas to get one object.
		if cand.depth >= pw.depth {
			continue
		}
		d := MakeDelta(cand.payload, o.Payload)
		score := pw.score(len(d), cand.depth)
		if bestDelta == nil || score < bestScore {
			bestEntry, bestDelta, bestScore = cand, d, score
		}
	}

	// Only worth it if the delta is meaningfully smaller than the object.
	// Deltas that barely save anything still cost a read-time reconstruction,
	// so the threshold is deliberately not break-even.
	if bestDelta == nil || len(bestDelta) >= len(o.Payload)*9/10 {
		return nil, nil
	}
	return bestEntry, bestDelta
}

func (pw *Writer) writeWhole(o *object.Object) error {
	if err := pw.writeEntryHeader(uint8(o.Type), int64(len(o.Payload))); err != nil {
		return err
	}
	return pw.writeDeflated(o.Payload)
}

// writeDelta emits an offset-delta entry. start is where this entry's header
// begins, which is what the backward distance is measured from.
func (pw *Writer) writeDelta(start int64, base *windowEntry, delta []byte) error {
	// The size in a delta entry's header is the delta's length, not the
	// reconstructed object's.
	if err := pw.writeEntryHeader(typeOfsDelta, int64(len(delta))); err != nil {
		return err
	}
	// The distance runs from the start of this entry's header to the start
	// of the base's, not from the current write position. Measuring it after
	// the header has been written makes every delta point a few bytes short
	// of its base, which the reader rejects.
	if err := pw.writeOffsetDistance(start - base.offset); err != nil {
		return err
	}
	return pw.writeDeflated(delta)
}

// writeEntryHeader emits the variable-length type-and-size header.
func (pw *Writer) writeEntryHeader(typ uint8, size int64) error {
	var buf [16]byte
	n := 0

	b := byte(typ<<4) | byte(size&0x0f)
	size >>= 4
	for size > 0 {
		buf[n] = b | 0x80
		n++
		b = byte(size & 0x7f)
		size >>= 7
	}
	buf[n] = b
	n++

	w, err := pw.w.Write(buf[:n])
	pw.offset += int64(w)
	return err
}

// writeOffsetDistance emits the backward distance to a delta base.
//
// The encoding subtracts one at each continuation step, which removes the
// redundant representations a plain varint would have. It has to be built
// back to front because the most significant group is written first.
func (pw *Writer) writeOffsetDistance(dist int64) error {
	var tmp [16]byte
	i := len(tmp) - 1
	tmp[i] = byte(dist & 0x7f)
	for dist >>= 7; dist > 0; dist >>= 7 {
		dist--
		i--
		tmp[i] = 0x80 | byte(dist&0x7f)
	}
	n, err := pw.w.Write(tmp[i:])
	pw.offset += int64(n)
	return err
}

// countingWriter counts bytes so the writer can track the pack offset
// through a compressor that reports only its input length.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (pw *Writer) writeDeflated(payload []byte) error {
	cw := &countingWriter{w: pw.w}
	zw := zlib.NewWriter(cw)
	if _, err := zw.Write(payload); err != nil {
		return fmt.Errorf("pack: deflate: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("pack: deflate close: %w", err)
	}
	pw.offset += cw.n
	return nil
}

func (pw *Writer) pushWindow(e windowEntry) {
	if pw.window == 0 {
		return
	}
	pw.recent = append(pw.recent, e)
	if len(pw.recent) > pw.window {
		pw.recent = pw.recent[len(pw.recent)-pw.window:]
	}
}

// Close writes the trailing checksum and returns the pack's name, which is
// the SHA-1 of everything before the trailer.
func (pw *Writer) Close() (object.ID, error) {
	var id object.ID
	if pw.written != pw.count {
		return id, fmt.Errorf("pack: header declared %d objects but %d were written", pw.count, pw.written)
	}
	sum := pw.hash.Sum(nil)
	copy(id[:], sum)

	// The trailer is the checksum of everything before it, so it must not be
	// fed back into the hash. It goes straight to the destination, bypassing
	// the tee.
	if _, err := pw.raw.Write(sum); err != nil {
		return id, fmt.Errorf("pack: write trailer: %w", err)
	}
	pw.offset += trailerSize
	return id, nil
}

// WriteAll packs a set of objects in a sensible order and returns the pack's
// name.
//
// Order matters for compression, not correctness. Objects are grouped by
// type, then by the hash of the path they were first seen at, then by
// descending size, which is git's own order. Revisions of one file land next
// to each other, so the delta window has the right base in view without
// being widened.
func WriteAll(w io.Writer, objs []*object.Object) (object.ID, error) {
	sorted := Order(objs, PathHints(objs))

	pw, err := NewWriter(w, uint32(len(sorted)))
	if err != nil {
		return object.ID{}, err
	}
	for _, o := range sorted {
		if err := pw.WriteObject(o); err != nil {
			return object.ID{}, err
		}
	}
	return pw.Close()
}
