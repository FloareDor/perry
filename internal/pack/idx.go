package pack

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"sort"

	"github.com/perry-dev/perry/internal/object"
)

// Pack index v2 layout constants.
var idxMagic = []byte{0xff, 't', 'O', 'c'}

const (
	idxVersion = 2
	fanoutSize = 256 * 4

	// largeOffsetFlag marks a 4-byte offset slot as an index into the
	// 8-byte offset table instead of an offset itself. A pack larger than
	// 2 GiB cannot address all its entries in 31 bits, so the format spills
	// into a side table rather than widening every slot.
	largeOffsetFlag = 1 << 31
)

// IndexEntry is one object's position in a packfile.
type IndexEntry struct {
	ID     object.ID
	Offset int64
	CRC32  uint32
}

// Index is a parsed v2 pack index.
//
// Entries are held sorted by object name, which is what makes lookup a
// binary search rather than a scan. The fanout table narrows that search to
// the objects sharing a first byte before it begins.
type Index struct {
	Entries []IndexEntry
	PackID  object.ID
	fanout  [256]uint32
	byID    map[object.ID]int
}

// Count returns the number of indexed objects.
func (ix *Index) Count() int { return len(ix.Entries) }

// Lookup finds an object's offset in the pack.
func (ix *Index) Lookup(id object.ID) (int64, bool) {
	if ix.byID != nil {
		if i, ok := ix.byID[id]; ok {
			return ix.Entries[i].Offset, true
		}
		return 0, false
	}

	// Binary search within the first-byte bucket the fanout table names.
	lo := 0
	if id[0] > 0 {
		lo = int(ix.fanout[id[0]-1])
	}
	hi := int(ix.fanout[id[0]])
	if lo < 0 || hi > len(ix.Entries) || lo > hi {
		return 0, false
	}

	i := sort.Search(hi-lo, func(k int) bool {
		return ix.Entries[lo+k].ID.Compare(id) >= 0
	})
	if lo+i < hi && ix.Entries[lo+i].ID == id {
		return ix.Entries[lo+i].Offset, true
	}
	return 0, false
}

// Has reports whether the pack contains an object.
func (ix *Index) Has(id object.ID) bool {
	_, ok := ix.Lookup(id)
	return ok
}

// IDs returns every object name in the index, in sorted order.
func (ix *Index) IDs() []object.ID {
	out := make([]object.ID, len(ix.Entries))
	for i, e := range ix.Entries {
		out[i] = e.ID
	}
	return out
}

// BuildIndex scans a packfile and produces its index.
//
// The scan has to resolve every object, because an entry's name depends on
// its content and a delta's content depends on its base. Entries appear in
// offset order and a delta's base always precedes it, so one forward pass is
// enough.
func BuildIndex(p *Reader) (*Index, error) {
	entries := make([]IndexEntry, 0, p.Count())

	byID := make(map[object.ID]int64, p.Count())
	p.mu.Lock()
	p.offsetByID = byID
	p.mu.Unlock()

	// CRC32 covers each entry's stored bytes, header and deflate stream
	// together, so it can only be computed once the entry's extent is known.
	// The scan records where each entry started and closes out the previous
	// one when the next begins.
	var prevOffset int64 = -1
	var prevIndex int

	finish := func(end int64) error {
		if prevOffset < 0 {
			return nil
		}
		raw := make([]byte, end-prevOffset)
		if _, err := p.r.ReadAt(raw, prevOffset); err != nil {
			return fmt.Errorf("pack: read entry bytes at %d: %w", prevOffset, err)
		}
		entries[prevIndex].CRC32 = crc32.ChecksumIEEE(raw)
		return nil
	}

	err := p.Scan(func(e Entry, data []byte) error {
		if err := finish(e.Offset); err != nil {
			return err
		}
		o, err := p.resolveEntry(e, data, 0)
		if err != nil {
			return err
		}
		id := o.ID()
		byID[id] = e.Offset

		prevIndex = len(entries)
		prevOffset = e.Offset
		entries = append(entries, IndexEntry{ID: id, Offset: e.Offset})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The final entry ends where the trailer begins.
	if err := finish(p.size - trailerSize); err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID.Compare(entries[j].ID) < 0
	})

	ix := &Index{Entries: entries}
	ix.buildFanout()

	// The pack's name is its trailing checksum.
	var packID object.ID
	buf := make([]byte, trailerSize)
	if _, err := p.r.ReadAt(buf, p.size-trailerSize); err != nil {
		return nil, fmt.Errorf("pack: read pack checksum: %w", err)
	}
	copy(packID[:], buf)
	ix.PackID = packID

	return ix, nil
}

func (ix *Index) buildFanout() {
	// fanout[b] is the number of objects whose first byte is at most b, so
	// the bucket for byte b runs from fanout[b-1] to fanout[b].
	var counts [256]uint32
	for _, e := range ix.Entries {
		counts[e.ID[0]]++
	}
	var running uint32
	for i := 0; i < 256; i++ {
		running += counts[i]
		ix.fanout[i] = running
	}
}

// WriteTo serializes the index in v2 format.
func (ix *Index) WriteTo(w io.Writer) (int64, error) {
	h := sha1.New()
	mw := io.MultiWriter(w, h)
	var n int64

	write := func(b []byte) error {
		k, err := mw.Write(b)
		n += int64(k)
		return err
	}

	if err := write(idxMagic); err != nil {
		return n, err
	}
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], idxVersion)
	if err := write(u32[:]); err != nil {
		return n, err
	}

	for i := 0; i < 256; i++ {
		binary.BigEndian.PutUint32(u32[:], ix.fanout[i])
		if err := write(u32[:]); err != nil {
			return n, err
		}
	}

	// The three per-object tables are stored column by column rather than
	// row by row, so a reader that only needs names never touches the
	// offsets.
	for _, e := range ix.Entries {
		if err := write(e.ID[:]); err != nil {
			return n, err
		}
	}
	for _, e := range ix.Entries {
		binary.BigEndian.PutUint32(u32[:], e.CRC32)
		if err := write(u32[:]); err != nil {
			return n, err
		}
	}

	var large []int64
	for _, e := range ix.Entries {
		if e.Offset < largeOffsetFlag {
			binary.BigEndian.PutUint32(u32[:], uint32(e.Offset))
		} else {
			binary.BigEndian.PutUint32(u32[:], uint32(largeOffsetFlag|len(large)))
			large = append(large, e.Offset)
		}
		if err := write(u32[:]); err != nil {
			return n, err
		}
	}
	var u64 [8]byte
	for _, off := range large {
		binary.BigEndian.PutUint64(u64[:], uint64(off))
		if err := write(u64[:]); err != nil {
			return n, err
		}
	}

	if err := write(ix.PackID[:]); err != nil {
		return n, err
	}
	// The index's own checksum covers everything before it, so it goes
	// straight to w rather than through the hashing tee.
	sum := h.Sum(nil)
	k, err := w.Write(sum)
	n += int64(k)
	return n, err
}

// ReadIndex parses a v2 pack index.
func ReadIndex(data []byte) (*Index, error) {
	const minSize = 8 + fanoutSize + 2*object.IDSize
	if len(data) < minSize {
		return nil, fmt.Errorf("pack: index is %d bytes, too short", len(data))
	}
	if !bytes.Equal(data[0:4], idxMagic) {
		return nil, fmt.Errorf("pack: index has bad magic %x (v1 indexes are not supported)", data[0:4])
	}
	if v := binary.BigEndian.Uint32(data[4:8]); v != idxVersion {
		return nil, fmt.Errorf("pack: unsupported index version %d", v)
	}

	// Verify the trailing checksum before trusting any offset in the file.
	body := data[:len(data)-object.IDSize]
	want := data[len(data)-object.IDSize:]
	if got := sha1.Sum(body); !bytes.Equal(got[:], want) {
		return nil, fmt.Errorf("pack: index checksum mismatch")
	}

	ix := &Index{}
	pos := 8
	for i := 0; i < 256; i++ {
		ix.fanout[i] = binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
	}
	count := int(ix.fanout[255])

	// A corrupt fanout could claim an enormous count. Check the file is
	// actually big enough before allocating for it.
	need := pos + count*(object.IDSize+4+4) + 2*object.IDSize
	if count < 0 || need > len(data) {
		return nil, fmt.Errorf("pack: index claims %d objects but is only %d bytes", count, len(data))
	}

	ix.Entries = make([]IndexEntry, count)
	for i := 0; i < count; i++ {
		copy(ix.Entries[i].ID[:], data[pos:pos+object.IDSize])
		pos += object.IDSize
	}
	for i := 0; i < count; i++ {
		ix.Entries[i].CRC32 = binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
	}

	smallOffsets := make([]uint32, count)
	for i := 0; i < count; i++ {
		smallOffsets[i] = binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
	}

	// The large-offset table sits between the offsets and the two trailing
	// checksums, so its length is whatever is left over.
	largeBytes := len(data) - 2*object.IDSize - pos
	if largeBytes < 0 || largeBytes%8 != 0 {
		return nil, fmt.Errorf("pack: index large-offset table is %d bytes, not a multiple of 8", largeBytes)
	}
	largeCount := largeBytes / 8
	for i := 0; i < count; i++ {
		v := smallOffsets[i]
		if v&largeOffsetFlag == 0 {
			ix.Entries[i].Offset = int64(v)
			continue
		}
		k := int(v &^ largeOffsetFlag)
		if k >= largeCount {
			return nil, fmt.Errorf("pack: index entry %d points at large offset %d of %d", i, k, largeCount)
		}
		ix.Entries[i].Offset = int64(binary.BigEndian.Uint64(data[pos+k*8 : pos+k*8+8]))
	}
	pos += largeBytes

	copy(ix.PackID[:], data[pos:pos+object.IDSize])
	return ix, nil
}

// Name returns the index filename git would use, which is the pack's name.
func (ix *Index) Name() string { return "pack-" + ix.PackID.String() }
