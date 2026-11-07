package pack

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIndexRoundTrip(t *testing.T) {
	objs := sampleObjects(300)
	var buf bytes.Buffer
	if _, err := WriteAll(&buf, objs); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := BuildIndex(r)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Count() != len(objs) {
		t.Fatalf("indexed %d, wrote %d", ix.Count(), len(objs))
	}

	var idxBuf bytes.Buffer
	if _, err := ix.WriteTo(&idxBuf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(idxBuf.Bytes())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Count() != ix.Count() || got.PackID != ix.PackID {
		t.Fatalf("round trip changed the index")
	}
	for _, want := range ix.Entries {
		off, ok := got.Lookup(want.ID)
		if !ok {
			t.Fatalf("%s missing after round trip", want.ID)
		}
		if off != want.Offset {
			t.Errorf("%s: offset %d, want %d", want.ID, off, want.Offset)
		}
		// The offset must actually resolve to the object it is filed under.
		o, err := r.ObjectAt(off)
		if err != nil {
			t.Fatalf("resolve %s: %v", want.ID, err)
		}
		if o.ID() != want.ID {
			t.Errorf("offset %d holds %s, indexed as %s", off, o.ID(), want.ID)
		}
	}

	// Entries must be sorted by name, or the binary search is meaningless.
	for i := 1; i < len(got.Entries); i++ {
		if got.Entries[i-1].ID.Compare(got.Entries[i].ID) >= 0 {
			t.Fatalf("index is not sorted at %d", i)
		}
	}

	var absent ObjectIDForTest
	_ = absent
	if got.Has(objs[0].ID()) == false {
		t.Error("Has said no for an object that is present")
	}
}

type ObjectIDForTest struct{}

func TestReadIndexRejectsCorruption(t *testing.T) {
	var buf bytes.Buffer
	if _, err := WriteAll(&buf, sampleObjects(20)); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	ix, err := BuildIndex(r)
	if err != nil {
		t.Fatal(err)
	}
	var idxBuf bytes.Buffer
	if _, err := ix.WriteTo(&idxBuf); err != nil {
		t.Fatal(err)
	}
	good := idxBuf.Bytes()

	for _, tc := range []struct {
		name   string
		break_ func([]byte) []byte
	}{
		{"bad magic", func(b []byte) []byte { c := append([]byte{}, b...); c[0] = 0; return c }},
		{"bad version", func(b []byte) []byte { c := append([]byte{}, b...); c[7] = 9; return c }},
		{"truncated", func(b []byte) []byte { return b[:len(b)/2] }},
		{"flipped byte", func(b []byte) []byte { c := append([]byte{}, b...); c[len(c)/2] ^= 0xff; return c }},
		{"bad checksum", func(b []byte) []byte { c := append([]byte{}, b...); c[len(c)-1] ^= 0xff; return c }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadIndex(tc.break_(good)); err == nil {
				t.Error("corrupt index accepted")
			}
		})
	}
}

// TestIndexMatchesGit checks our index against the one git builds for the
// same pack: same object count, same names, same offsets.
func TestIndexMatchesGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	matches, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "*.git", "objects", "pack", "*.pack"))
	if len(matches) == 0 {
		t.Skip("no fixture pack; clone one into testdata/")
	}
	packPath := matches[0]

	f, err := os.Open(packPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	r, err := NewReader(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	ours, err := BuildIndex(r)
	if err != nil {
		t.Fatal(err)
	}

	// git verify-pack lists every object with its offset.
	out, err := exec.Command(git, "verify-pack", "-v", packPath).Output()
	if err != nil {
		t.Fatalf("git verify-pack: %v", err)
	}
	gitOffsets := map[string]int64{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		fields := bytes.Fields(line)
		// "<sha1> <type> <size> <packed size> <offset> [depth base]"
		if len(fields) < 5 || len(fields[0]) != 40 {
			continue
		}
		var off int64
		if _, err := fmtSscan(string(fields[4]), &off); err != nil {
			continue
		}
		gitOffsets[string(fields[0])] = off
	}
	if len(gitOffsets) == 0 {
		t.Fatal("parsed no objects out of git verify-pack")
	}
	if ours.Count() != len(gitOffsets) {
		t.Errorf("we indexed %d objects, git lists %d", ours.Count(), len(gitOffsets))
	}
	for sha, gitOff := range gitOffsets {
		id, err := parseIDForTest(sha)
		if err != nil {
			t.Fatal(err)
		}
		off, ok := ours.Lookup(id)
		if !ok {
			t.Fatalf("%s is in git's index and not ours", sha)
		}
		if off != gitOff {
			t.Fatalf("%s: our offset %d, git's %d", sha, off, gitOff)
		}
	}
	t.Logf("matched git on all %d objects and offsets", len(gitOffsets))

	// Our index must also survive git's own reader.
	dir := t.TempDir()
	base := filepath.Join(dir, "pack-"+ours.PackID.String())
	packBytes, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".pack", packBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var idxBuf bytes.Buffer
	if _, err := ours.WriteTo(&idxBuf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".idx", idxBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(git, "verify-pack", "-s", base+".pack")
	if res, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rejected our index: %v\n%s", err, res)
	}
	t.Log("git verify-pack accepted our index")
}
