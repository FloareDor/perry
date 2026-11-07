package pack

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestDeltaRoundTrip(t *testing.T) {
	cases := []struct {
		name         string
		base, target []byte
	}{
		{"identical", []byte("hello world"), []byte("hello world")},
		{"empty base", nil, []byte("brand new content")},
		{"empty target", []byte("gone"), nil},
		{"both empty", nil, nil},
		{"append", []byte("line one\n"), []byte("line one\nline two\n")},
		{"prepend", []byte("line two\n"), []byte("line one\nline two\n")},
		{"middle edit", []byte("aaaaaaaaaaaaaaaaBBBBcccccccccccccccc"), []byte("aaaaaaaaaaaaaaaaZZZZcccccccccccccccc")},
		{"unrelated", []byte("completely different"), []byte("nothing in common here at all")},
		{"long repeat", bytes.Repeat([]byte("abcdefgh"), 5000), bytes.Repeat([]byte("abcdefgh"), 5001)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := MakeDelta(tc.base, tc.target)
			got, err := ApplyDelta(tc.base, d)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if !bytes.Equal(got, tc.target) {
				t.Errorf("round trip mismatch: got %d bytes, want %d", len(got), len(tc.target))
			}
		})
	}
}

// A copy of exactly 64 KiB is the one length the size field cannot hold, so
// it is encoded as zero. This is the classic place to get the format wrong.
func TestDeltaCopyOf64KiB(t *testing.T) {
	base := bytes.Repeat([]byte("q"), 0x10000)
	target := make([]byte, 0, 0x10000+4)
	target = append(target, base...)
	target = append(target, "tail"...)

	d := MakeDelta(base, target)
	got, err := ApplyDelta(base, d)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !bytes.Equal(got, target) {
		t.Fatal("64 KiB copy did not round trip")
	}
}

func TestDeltaBeatsLiteralOnSmallEdits(t *testing.T) {
	// The point of a delta is size. A one-line change to a large file should
	// produce a delta far smaller than the file.
	base := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 2000)
	target := append(append([]byte{}, base...), "one extra line\n"...)

	d := MakeDelta(base, target)
	if len(d) > len(target)/50 {
		t.Errorf("delta is %d bytes for a %d-byte target; expected under %d",
			len(d), len(target), len(target)/50)
	}
}

func TestApplyDeltaRejectsCorruption(t *testing.T) {
	base := []byte("0123456789")
	valid := MakeDelta(base, []byte("0123456789extra"))

	t.Run("wrong base size", func(t *testing.T) {
		if _, err := ApplyDelta([]byte("short"), valid); err == nil {
			t.Error("mismatched base accepted")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, err := ApplyDelta(base, valid[:2]); err == nil {
			t.Error("truncated delta accepted")
		}
	})
	t.Run("reserved opcode", func(t *testing.T) {
		d := []byte{10, 10, 0x00} // base size 10, target size 10, opcode 0
		if _, err := ApplyDelta(base, d); err == nil {
			t.Error("reserved opcode 0 accepted")
		}
	})
	t.Run("copy past end of base", func(t *testing.T) {
		// Copy offset 0, size 0xff00, from a 10-byte base.
		d := []byte{10, 0, 0x80 | 0x20, 0xff}
		if _, err := ApplyDelta(base, d); err == nil {
			t.Error("out-of-range copy accepted")
		}
	})
	t.Run("size disagrees with output", func(t *testing.T) {
		// Declares a 99-byte target but inserts only 2 bytes.
		d := []byte{10, 99, 0x02, 'h', 'i'}
		if _, err := ApplyDelta(base, d); err == nil {
			t.Error("short output accepted")
		}
	})
}

func TestDeltaVarintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 255, 16384, 1 << 31, 1 << 62} {
		b := writeDeltaVarint(nil, v)
		got, n := readDeltaVarint(b)
		if n != len(b) || got != v {
			t.Errorf("varint %d: got %d after %d/%d bytes", v, got, n, len(b))
		}
	}
	if _, n := readDeltaVarint([]byte{0x80}); n != 0 {
		t.Error("truncated varint should report zero bytes consumed")
	}
}

func TestDeltaFuzzRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		base := randBytes(rng, rng.Intn(4000))
		target := mutate(rng, base)
		d := MakeDelta(base, target)
		got, err := ApplyDelta(base, d)
		if err != nil {
			t.Fatalf("case %d: apply: %v", i, err)
		}
		if !bytes.Equal(got, target) {
			t.Fatalf("case %d: mismatch (base %d, target %d)", i, len(base), len(target))
		}
	}
}

func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rng.Intn(6))
	}
	return b
}

// mutate derives a plausible "next version" of b: some edits keep most of the
// content, which is the case deltas are built for.
func mutate(rng *rand.Rand, b []byte) []byte {
	out := append([]byte{}, b...)
	for i := 0; i < rng.Intn(8); i++ {
		if len(out) == 0 {
			out = append(out, randBytes(rng, rng.Intn(20))...)
			continue
		}
		switch rng.Intn(3) {
		case 0: // insert
			at := rng.Intn(len(out))
			ins := randBytes(rng, 1+rng.Intn(30))
			out = append(out[:at], append(ins, out[at:]...)...)
		case 1: // delete
			at := rng.Intn(len(out))
			n := rng.Intn(len(out) - at)
			out = append(out[:at], out[at+n:]...)
		case 2: // overwrite
			at := rng.Intn(len(out))
			out[at] = byte('A' + rng.Intn(6))
		}
	}
	return out
}

func BenchmarkMakeDelta(b *testing.B) {
	base := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 5000)
	target := append(append([]byte{}, base...), "tail\n"...)
	b.SetBytes(int64(len(target)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MakeDelta(base, target)
	}
}

func BenchmarkApplyDelta(b *testing.B) {
	base := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 5000)
	target := append(append([]byte{}, base...), "tail\n"...)
	d := MakeDelta(base, target)
	b.SetBytes(int64(len(target)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ApplyDelta(base, d); err != nil {
			b.Fatal(err)
		}
	}
}
