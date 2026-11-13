// Package pack implements git's packfile format: the reader that resolves
// delta chains, the writer that produces packs for clone and fetch, and the
// v2 index that maps object names to pack offsets.
package pack

import (
	"errors"
	"fmt"
)

// ErrBadDelta reports a corrupt or inconsistent delta stream.
var ErrBadDelta = errors.New("pack: malformed delta")

// MaxDepth bounds how many deltas may chain before a full object is reached.
// Git's own packs stay under 50. A corrupt or hostile pack can describe a
// cycle, and without this bound resolving it would recurse until the process
// died.
//
// It bounds the writer as well as the reader: SetDepth clamps to it, so this
// package cannot produce a pack it would refuse to read back.
const MaxDepth = 100

// ApplyDelta reconstructs a target object from a base and a delta stream.
//
// A delta is a short program: a header giving the expected base and result
// sizes, then instructions that either copy a run of bytes from the base or
// insert literal bytes carried in the delta itself. Most of a typical object
// is unchanged from its base, so the copies dominate and the delta stays
// small.
func ApplyDelta(base, delta []byte) ([]byte, error) {
	pos := 0

	baseSize, n := readDeltaVarint(delta[pos:])
	if n == 0 {
		return nil, fmt.Errorf("%w: truncated base size", ErrBadDelta)
	}
	pos += n
	if baseSize != uint64(len(base)) {
		return nil, fmt.Errorf("%w: delta expects a %d-byte base, got %d", ErrBadDelta, baseSize, len(base))
	}

	targetSize, n := readDeltaVarint(delta[pos:])
	if n == 0 {
		return nil, fmt.Errorf("%w: truncated target size", ErrBadDelta)
	}
	pos += n

	// The target size is declared up front, so the result buffer is allocated
	// once at exactly the right size and never grown.
	out := make([]byte, 0, targetSize)

	for pos < len(delta) {
		op := delta[pos]
		pos++

		switch {
		case op&0x80 != 0:
			// Copy from the base. The low seven bits say which of the four
			// offset bytes and three size bytes are present; absent bytes are
			// zero. This is how a copy of a far-away, large run still fits in
			// a few bytes.
			var off, size uint64
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("%w: truncated copy offset", ErrBadDelta)
					}
					off |= uint64(delta[pos]) << (8 * uint(i))
					pos++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("%w: truncated copy size", ErrBadDelta)
					}
					size |= uint64(delta[pos]) << (8 * uint(i))
					pos++
				}
			}
			// A size of zero means 64 KiB. The encoding has no other way to
			// express that length in three bytes.
			if size == 0 {
				size = 0x10000
			}
			if off+size < off || off+size > uint64(len(base)) {
				return nil, fmt.Errorf("%w: copy [%d,%d) is outside a %d-byte base", ErrBadDelta, off, off+size, len(base))
			}
			out = append(out, base[off:off+size]...)

		case op != 0:
			// Insert: the opcode itself is the length, 1..127.
			size := int(op)
			if pos+size > len(delta) {
				return nil, fmt.Errorf("%w: insert of %d bytes runs past the delta", ErrBadDelta, size)
			}
			out = append(out, delta[pos:pos+size]...)
			pos += size

		default:
			// Opcode 0 is reserved and never emitted by git.
			return nil, fmt.Errorf("%w: reserved opcode 0 at offset %d", ErrBadDelta, pos-1)
		}
	}

	if uint64(len(out)) != targetSize {
		return nil, fmt.Errorf("%w: delta produced %d bytes, header declared %d", ErrBadDelta, len(out), targetSize)
	}
	return out, nil
}

// readDeltaVarint decodes the little-endian base-128 varint used for the two
// sizes in a delta header. It returns the value and the bytes consumed, or a
// zero count if the input is truncated.
func readDeltaVarint(b []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i := 0; i < len(b); i++ {
		if shift >= 64 {
			return 0, 0
		}
		v |= uint64(b[i]&0x7f) << shift
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
	}
	return 0, 0
}

// writeDeltaVarint appends the little-endian base-128 form of v.
func writeDeltaVarint(dst []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if v == 0 {
			return dst
		}
	}
}

// MakeDelta encodes target as a delta against base.
//
// The matcher indexes the base in fixed blocks and looks each block of the
// target up in that index, extending a hit in both directions. It is the
// same shape as git's own delta search but with a simpler hash and no
// attempt to pick the best of several candidates, which costs some ratio and
// buys a great deal of simplicity.
func MakeDelta(base, target []byte) []byte {
	const blockSize = 16

	out := make([]byte, 0, len(target)/2+32)
	out = writeDeltaVarint(out, uint64(len(base)))
	out = writeDeltaVarint(out, uint64(len(target)))

	// Index the base, keeping several candidate offsets per hash rather than
	// one.
	//
	// Keeping only the last offset is much worse than it sounds on repetitive
	// content. If the base is a line repeated many times, every block hash
	// resolves to its final occurrence near the end of the base, so a copy
	// that could have spanned the whole file stops a few bytes later and the
	// delta fragments into thousands of short copies. Holding the first few
	// offsets and extending each lets the match that starts early win.
	const maxCandidates = 4
	index := make(map[uint32][]int32, len(base)/blockSize+1)
	for i := 0; i+blockSize <= len(base); i += blockSize {
		h := blockHash(base[i : i+blockSize])
		if len(index[h]) < maxCandidates {
			index[h] = append(index[h], int32(i))
		}
	}

	var literal []byte
	flush := func() {
		// Literal runs are emitted in chunks of 127, the largest an insert
		// opcode can describe.
		for len(literal) > 0 {
			n := len(literal)
			if n > 127 {
				n = 127
			}
			out = append(out, byte(n))
			out = append(out, literal[:n]...)
			literal = literal[n:]
		}
	}

	pos := 0
	for pos < len(target) {
		if pos+blockSize > len(target) {
			literal = append(literal, target[pos:]...)
			break
		}

		// Try every candidate and keep the longest match. Each hit is
		// verified byte for byte first, since the hash can collide.
		bestOff, bestLen := -1, 0
		for _, cand := range index[blockHash(target[pos:pos+blockSize])] {
			off := int(cand)
			if !equalRun(base[off:], target[pos:], blockSize) {
				continue
			}
			length := blockSize
			for off+length < len(base) && pos+length < len(target) && base[off+length] == target[pos+length] {
				length++
			}
			if length > bestLen {
				bestOff, bestLen = off, length
			}
		}
		if bestOff < 0 {
			literal = append(literal, target[pos])
			pos++
			continue
		}

		flush()
		out = appendCopy(out, uint64(bestOff), uint64(bestLen))
		pos += bestLen
	}
	flush()
	return out
}

// appendCopy emits one or more copy instructions for a run, splitting at the
// 64 KiB maximum a single copy can express.
func appendCopy(dst []byte, off, size uint64) []byte {
	for size > 0 {
		n := size
		if n > 0x10000 {
			n = 0x10000
		}

		// Build the opcode and its operand bytes together: a byte is written
		// only when it is non-zero, and its bit is set in the opcode.
		opIndex := len(dst)
		dst = append(dst, 0x80)
		op := byte(0x80)

		for i := 0; i < 4; i++ {
			if b := byte(off >> (8 * uint(i))); b != 0 {
				op |= 1 << uint(i)
				dst = append(dst, b)
			}
		}
		// A full 64 KiB copy is encoded as size zero with no size bytes.
		if n != 0x10000 {
			for i := 0; i < 3; i++ {
				if b := byte(n >> (8 * uint(i))); b != 0 {
					op |= 0x10 << uint(i)
					dst = append(dst, b)
				}
			}
		}
		dst[opIndex] = op

		off += n
		size -= n
	}
	return dst
}

// blockHash is an FNV-1a hash over a fixed-size block. It only has to spread
// well enough to index the base; every hit is verified byte for byte.
func blockHash(b []byte) uint32 {
	var h uint32 = 2166136261
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

func equalRun(a, b []byte, n int) bool {
	if len(a) < n || len(b) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
