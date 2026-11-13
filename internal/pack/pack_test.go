package pack

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perry-dev/perry/internal/object"
)

func TestWriteReadRoundTrip(t *testing.T) {
	objs := sampleObjects(200)

	var buf bytes.Buffer
	id, err := WriteAll(&buf, objs)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if id.IsZero() {
		t.Error("pack name is zero")
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if int(r.Count()) != len(objs) {
		t.Fatalf("header says %d objects, wrote %d", r.Count(), len(objs))
	}
	if err := r.VerifyChecksum(); err != nil {
		t.Errorf("checksum: %v", err)
	}

	byID, err := r.BuildOffsetIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(byID) != len(objs) {
		t.Errorf("indexed %d objects, wrote %d", len(byID), len(objs))
	}

	for _, want := range objs {
		off, ok := byID[want.ID()]
		if !ok {
			t.Fatalf("object %s missing from the pack", want.ID())
		}
		got, err := r.ObjectAt(off)
		if err != nil {
			t.Fatalf("read %s: %v", want.ID(), err)
		}
		if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("object %s came back wrong", want.ID())
		}
		if got.ID() != want.ID() {
			t.Errorf("id changed: %s -> %s", want.ID(), got.ID())
		}
	}
}

// Deltas must actually be used, or the writer is just a container format.
func TestWriterProducesDeltas(t *testing.T) {
	// Twenty near-identical blobs: everything after the first should be a
	// delta against one of its neighbours.
	base := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 300)
	var objs []*object.Object
	for i := 0; i < 20; i++ {
		p := append(append([]byte{}, base...), []byte(fmt.Sprintf("line %d\n", i))...)
		objs = append(objs, &object.Object{Type: object.TypeBlob, Payload: p})
	}

	var withDelta, withoutDelta bytes.Buffer
	if _, err := WriteAll(&withDelta, objs); err != nil {
		t.Fatal(err)
	}

	pw, err := NewWriter(&withoutDelta, uint32(len(objs)))
	if err != nil {
		t.Fatal(err)
	}
	pw.SetWindow(0)
	for _, o := range objs {
		if err := pw.WriteObject(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pw.Close(); err != nil {
		t.Fatal(err)
	}

	t.Logf("deltified %d bytes, whole %d bytes", withDelta.Len(), withoutDelta.Len())
	if withDelta.Len() >= withoutDelta.Len()/2 {
		t.Errorf("deltification saved almost nothing: %d vs %d bytes",
			withDelta.Len(), withoutDelta.Len())
	}

	// Count how many entries are stored as deltas.
	r, err := NewReader(bytes.NewReader(withDelta.Bytes()), int64(withDelta.Len()))
	if err != nil {
		t.Fatal(err)
	}
	deltas := 0
	if err := r.Scan(func(e Entry, _ []byte) error {
		if e.IsDelta() {
			deltas++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deltas < len(objs)-1 {
		t.Errorf("only %d of %d entries are deltas", deltas, len(objs)-1)
	}
}

func TestWriterRejectsWrongCount(t *testing.T) {
	var buf bytes.Buffer
	pw, err := NewWriter(&buf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := pw.WriteObject(&object.Object{Type: object.TypeBlob, Payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	// Closing with one of two objects written must fail: a pack whose header
	// disagrees with its body is unreadable.
	if _, err := pw.Close(); err == nil {
		t.Error("closing an incomplete pack succeeded")
	}

	buf.Reset()
	pw, _ = NewWriter(&buf, 1)
	_ = pw.WriteObject(&object.Object{Type: object.TypeBlob, Payload: []byte("a")})
	if err := pw.WriteObject(&object.Object{Type: object.TypeBlob, Payload: []byte("b")}); err == nil {
		t.Error("writing past the declared count succeeded")
	}
}

func TestReaderRejectsCorruption(t *testing.T) {
	var buf bytes.Buffer
	if _, err := WriteAll(&buf, sampleObjects(10)); err != nil {
		t.Fatal(err)
	}
	good := buf.Bytes()

	t.Run("bad magic", func(t *testing.T) {
		b := append([]byte{}, good...)
		copy(b[0:4], "NOPE")
		if _, err := NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Error("accepted bad magic")
		}
	})
	t.Run("bad version", func(t *testing.T) {
		b := append([]byte{}, good...)
		b[7] = 9
		if _, err := NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Error("accepted version 9")
		}
	})
	t.Run("too short", func(t *testing.T) {
		if _, err := NewReader(bytes.NewReader(good[:8]), 8); err == nil {
			t.Error("accepted an 8-byte pack")
		}
	})
	t.Run("corrupt trailer", func(t *testing.T) {
		b := append([]byte{}, good...)
		b[len(b)-1] ^= 0xff
		r, err := NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.VerifyChecksum(); err == nil {
			t.Error("accepted a corrupt trailer")
		}
	})
	t.Run("corrupt body", func(t *testing.T) {
		b := append([]byte{}, good...)
		b[len(b)/2] ^= 0xff
		r, err := NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.VerifyChecksum(); err == nil {
			t.Error("accepted a corrupt body")
		}
	})
}

// TestAgainstRealGit is the test that proves the format is right. Agreeing
// with our own writer proves only that the two are consistent.
func TestAgainstRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Perry", "GIT_AUTHOR_EMAIL=perry@example.com",
			"GIT_COMMITTER_NAME=Perry", "GIT_COMMITTER_EMAIL=perry@example.com",
			"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// Build a repo with enough similar content that git will deltify, and
	// enough history that trees and commits are exercised too.
	run("init", "-q")
	run("config", "user.email", "perry@example.com")
	run("config", "user.name", "Perry")
	line := "the quick brown fox jumps over the lazy dog\n"
	for i := 0; i < 15; i++ {
		body := strings.Repeat(line, 400) + fmt.Sprintf("revision %d\n", i)
		if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "src", "pkg")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "a.go"), []byte(fmt.Sprintf("package pkg\n\nconst N = %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-q", "-m", fmt.Sprintf("revision %d", i))
	}
	run("gc", "-q", "--aggressive")

	packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("no packfile produced: %v", err)
	}

	t.Run("read git's pack", func(t *testing.T) {
		f, err := os.Open(packs[0])
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		st, _ := f.Stat()

		r, err := NewReader(f, st.Size())
		if err != nil {
			t.Fatalf("open git's pack: %v", err)
		}
		if err := r.VerifyChecksum(); err != nil {
			t.Fatalf("git's pack failed our checksum: %v", err)
		}

		byID, err := r.BuildOffsetIndex()
		if err != nil {
			t.Fatalf("index git's pack: %v", err)
		}
		if len(byID) == 0 {
			t.Fatal("indexed no objects")
		}
		t.Logf("read %d objects from a %d-byte pack git wrote", len(byID), st.Size())

		// Every object we recovered must hash to the name we filed it under,
		// and git must agree that the object exists with that type.
		checked := 0
		for id, off := range byID {
			o, err := r.ObjectAt(off)
			if err != nil {
				t.Fatalf("read %s: %v", id, err)
			}
			if o.ID() != id {
				t.Fatalf("object at %d hashes to %s, indexed as %s", off, o.ID(), id)
			}
			if checked < 40 {
				gotType := run("cat-file", "-t", id.String())
				if gotType != o.Type.String() {
					t.Errorf("%s: perry says %s, git says %s", id, o.Type, gotType)
				}
				checked++
			}
		}

		// Delta entries must be present, or this test is not exercising the
		// hard path at all.
		deltas, total := 0, 0
		if err := r.Scan(func(e Entry, _ []byte) error {
			total++
			if e.IsDelta() {
				deltas++
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d of %d entries in git's pack are deltas", deltas, total)
		if deltas == 0 {
			t.Error("git's pack contained no deltas; the delta path went untested")
		}
	})

	t.Run("git reads our pack", func(t *testing.T) {
		// Collect every object from the repo, write our own pack, and have
		// git verify and index it.
		ids := strings.Fields(run("rev-list", "--objects", "--all"))
		var objs []*object.Object
		seen := map[string]bool{}
		for _, tok := range ids {
			if len(tok) != 40 || seen[tok] {
				continue
			}
			if _, err := object.ParseID(tok); err != nil {
				continue
			}
			seen[tok] = true

			typ := run("cat-file", "-t", tok)
			cmd := exec.Command(git, "cat-file", typ, tok)
			cmd.Dir = dir
			payload, err := cmd.Output()
			if err != nil {
				t.Fatalf("cat-file %s: %v", tok, err)
			}
			ot, err := object.ParseType(typ)
			if err != nil {
				t.Fatal(err)
			}
			objs = append(objs, &object.Object{Type: ot, Payload: payload})
		}
		if len(objs) < 20 {
			t.Fatalf("only collected %d objects", len(objs))
		}

		var buf bytes.Buffer
		if _, err := WriteAll(&buf, objs); err != nil {
			t.Fatalf("write our pack: %v", err)
		}

		// index-pack is git's own validator: it re-reads the pack, resolves
		// every delta, and checks every hash. If it is happy, the pack is
		// correct by git's definition, not ours.
		out := filepath.Join(t.TempDir(), "perry.pack")
		if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(git, "index-pack", "-v", out)
		cmd.Dir = dir
		if res, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git index-pack rejected our %d-byte pack: %v\n%s", buf.Len(), err, res)
		}
		t.Logf("git accepted our pack: %d objects in %d bytes", len(objs), buf.Len())
	})
}

func sampleObjects(n int) []*object.Object {
	rng := rand.New(rand.NewSource(7))
	base := []byte(strings.Repeat("package main\n\nfunc main() {}\n", 40))

	objs := make([]*object.Object, 0, n)
	for i := 0; i < n; i++ {
		switch i % 4 {
		case 0:
			objs = append(objs, &object.Object{Type: object.TypeBlob, Payload: []byte(fmt.Sprintf("small %d\n", i))})
		case 1:
			p := append(append([]byte{}, base...), []byte(fmt.Sprintf("// variant %d\n", i))...)
			objs = append(objs, &object.Object{Type: object.TypeBlob, Payload: p})
		case 2:
			tr := &object.Tree{Entries: []object.TreeEntry{
				{Mode: object.ModeRegular, Name: fmt.Sprintf("f%d.txt", i), ID: object.Hash(object.TypeBlob, []byte{byte(i)})},
				{Mode: object.ModeDir, Name: "sub", ID: object.Hash(object.TypeTree, nil)},
			}}
			objs = append(objs, tr.Object())
		default:
			p := make([]byte, rng.Intn(500)+10)
			rng.Read(p)
			objs = append(objs, &object.Object{Type: object.TypeBlob, Payload: p})
		}
	}
	return objs
}

// chainDepths maps every entry offset in a pack to the length of its delta
// chain, without resolving anything.
func chainDepths(t *testing.T, data []byte) map[int64]int {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	baseOf := map[int64]int64{}
	var all []int64
	if err := r.Scan(func(e Entry, _ []byte) error {
		all = append(all, e.Offset)
		if e.IsDelta() {
			baseOf[e.Offset] = e.BaseOffset
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := make(map[int64]int, len(all))
	for _, off := range all {
		d, cur := 0, off
		for {
			b, ok := baseOf[cur]
			if !ok {
				break
			}
			d++
			cur = b
			if d > 10*MaxDepth {
				t.Fatalf("cycle in delta chain at %d", off)
			}
		}
		out[off] = d
	}
	return out
}

// TestWriterNeverExceedsReaderDepth is a regression test for a writer that
// would emit packs its own reader refuses.
//
// SetDepth used to take any value. Asking for a depth past the reader's limit
// produced a pack whose deepest object could not be resolved at all: a cold
// read failed with "exceeds depth 100 (corrupt or cyclic)" on a pack that was
// neither corrupt nor cyclic. It was easy to miss, because resolving objects
// in pack order warms the reader's offset cache and the recursion never gets
// deep enough to trip the bound.
//
// So this reads the deepest object first, on a reader that has resolved
// nothing, which is the case that actually failed.
func TestWriterNeverExceedsReaderDepth(t *testing.T) {
	// A long run of near-identical blobs. With a window of one, each blob can
	// only delta against its predecessor, so chains grow as deep as the
	// writer will let them.
	base := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 200)
	var objs []*object.Object
	for i := 0; i < 400; i++ {
		p := append(append([]byte{}, base...), []byte(fmt.Sprintf("line %d\n", i))...)
		objs = append(objs, &object.Object{Type: object.TypeBlob, Payload: p})
	}

	// Including values far past the reader's bound, which is the bug.
	for _, depth := range []int{0, 1, 10, 50, MaxDepth, MaxDepth + 1, 1000, 1 << 20, -5} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			var buf bytes.Buffer
			pw, err := NewWriter(&buf, uint32(len(objs)))
			if err != nil {
				t.Fatal(err)
			}
			pw.SetWindow(1)
			pw.SetDepth(depth)
			for _, o := range objs {
				if err := pw.WriteObject(o); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pw.Close(); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()

			depths := chainDepths(t, data)
			var deepest int64
			worst := -1
			for off, d := range depths {
				if d > worst {
					worst, deepest = d, off
				}
			}
			if worst > MaxDepth {
				t.Fatalf("SetDepth(%d) built a chain %d deep; the reader stops at %d",
					depth, worst, MaxDepth)
			}

			// Cold reader, deepest object first. This is what used to fail.
			cold, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cold.ObjectAt(deepest); err != nil {
				t.Fatalf("SetDepth(%d): deepest object (chain %d) is unreadable: %v",
					depth, worst, err)
			}

			// And every object round trips to the bytes that went in.
			warm, err := NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			byID, err := warm.BuildOffsetIndex()
			if err != nil {
				t.Fatal(err)
			}
			if len(byID) != len(objs) {
				t.Fatalf("pack holds %d objects, wrote %d", len(byID), len(objs))
			}
			for _, o := range objs {
				off, ok := byID[o.ID()]
				if !ok {
					t.Fatalf("object %s missing from the pack", o.ID().Short(8))
				}
				got, err := warm.ObjectAt(off)
				if err != nil {
					t.Fatalf("object %s: %v", o.ID().Short(8), err)
				}
				if !bytes.Equal(got.Payload, o.Payload) {
					t.Fatalf("object %s came back different", o.ID().Short(8))
				}
			}
		})
	}
}
