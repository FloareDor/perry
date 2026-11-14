package bench

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/pack"
)

// findFixturePack locates the packfile of a real repository cloned into
// testdata. Without it there is nothing honest to measure, so the benchmarks
// skip rather than invent numbers.
func findFixturePack(t testing.TB) string {
	t.Helper()
	// Pinned to one corpus on purpose. This used to glob every *.git under
	// testdata and take the first match, which silently changed the numbers
	// as soon as a second corpus was added.
	matches, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "cobra.git", "objects", "pack", "*.pack"))
	if len(matches) == 0 {
		t.Skip("no fixture pack in testdata/cobra.git. Fetch it with:\n" +
			"\tmake corpora      (or: sh scripts/corpora.sh)")
	}
	return matches[0]
}

// loadFixture reads every object out of a real packfile.
func loadFixture(t testing.TB) ([]*object.Object, int64) {
	t.Helper()
	path := findFixturePack(t)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()

	r, err := pack.NewReader(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	byID, err := r.BuildOffsetIndex()
	if err != nil {
		t.Fatal(err)
	}

	objs := make([]*object.Object, 0, len(byID))
	for _, off := range byID {
		o, err := r.ObjectAt(off)
		if err != nil {
			t.Fatal(err)
		}
		objs = append(objs, o)
	}
	return objs, st.Size()
}

// TestPackCompressionReport measures perry's pack size against git's own for
// the same object set, and against storing every object whole. It is written
// as a test rather than a benchmark so that it prints a table, but it is opt
// in like everything else here: see requireBench.
func TestPackCompressionReport(t *testing.T) {
	requireBench(t)
	objs, gitPackSize := loadFixture(t)

	var raw int64
	for _, o := range objs {
		raw += o.Size()
	}

	writeWith := func(window int) (int64, time.Duration) {
		var buf bytes.Buffer
		start := time.Now()
		pw, err := pack.NewWriter(&buf, uint32(len(objs)))
		if err != nil {
			t.Fatal(err)
		}
		pw.SetWindow(window)
		for _, o := range objs {
			if err := pw.WriteObject(o); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pw.Close(); err != nil {
			t.Fatal(err)
		}
		return int64(buf.Len()), time.Since(start)
	}

	whole, wholeDur := writeWith(0)
	w10, w10Dur := writeWith(10)

	var sortedBuf bytes.Buffer
	sortedStart := time.Now()
	if _, err := pack.WriteAll(&sortedBuf, objs); err != nil {
		t.Fatal(err)
	}
	sorted, sortedDur := int64(sortedBuf.Len()), time.Since(sortedStart)

	t.Logf("objects:            %d", len(objs))
	t.Logf("uncompressed:       %s", humanBytes(raw))
	t.Logf("perry, no deltas:   %s  (%.1fx)  in %v", humanBytes(whole), float64(raw)/float64(whole), wholeDur.Round(time.Millisecond))
	t.Logf("perry, window 10:   %s  (%.1fx)  in %v  (type and size order)", humanBytes(w10), float64(raw)/float64(w10), w10Dur.Round(time.Millisecond))
	t.Logf("perry, WriteAll:    %s  (%.1fx)  in %v  (type, path, size order)", humanBytes(sorted), float64(raw)/float64(sorted), sortedDur.Round(time.Millisecond))
	t.Logf("git's own pack:     %s  (%.1fx)", humanBytes(gitPackSize), float64(raw)/float64(gitPackSize))
	t.Logf("perry / git ratio:  %.2fx unsorted, %.2fx path-sorted", float64(w10)/float64(gitPackSize), float64(sorted)/float64(gitPackSize))

	if w10 >= whole {
		t.Errorf("deltification made the pack bigger: %d vs %d", w10, whole)
	}
}

// TestPackReadThroughput measures how fast perry resolves a whole real pack,
// deltas included.
func TestPackReadThroughput(t *testing.T) {
	requireBench(t)
	path := findFixturePack(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()

	start := time.Now()
	r, err := pack.NewReader(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	byID, err := r.BuildOffsetIndex()
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	deltas, total := 0, 0
	_ = r.Scan(func(e pack.Entry, _ []byte) error {
		total++
		if e.IsDelta() {
			deltas++
		}
		return nil
	})

	t.Logf("resolved %d objects (%d deltas, %.0f%%) from %s in %v = %s/s",
		len(byID), deltas, 100*float64(deltas)/float64(total),
		humanBytes(st.Size()), elapsed.Round(time.Millisecond),
		humanBytes(int64(float64(st.Size())/elapsed.Seconds())))
}

func BenchmarkResolveRealPack(b *testing.B) {
	path := findFixturePack(b)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := pack.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := r.BuildOffsetIndex(); err != nil {
			b.Fatal(err)
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// TestWindowSensitivity shows what the delta window buys, with objects in
// the path-aware order WriteAll uses. Before that order existed the packer
// needed a window of 250 to match git; with it the default of 10 is close.
func TestWindowSensitivity(t *testing.T) {
	requireBench(t)
	objs, gitPackSize := loadFixture(t)
	objs = pack.Order(objs, pack.PathHints(objs))

	var raw int64
	for _, o := range objs {
		raw += o.Size()
	}
	t.Logf("%d objects, %s uncompressed, git's pack is %s", len(objs), humanBytes(raw), humanBytes(gitPackSize))

	for _, window := range []int{0, 10, 25, 50, 100, 250} {
		var buf bytes.Buffer
		start := time.Now()
		pw, err := pack.NewWriter(&buf, uint32(len(objs)))
		if err != nil {
			t.Fatal(err)
		}
		pw.SetWindow(window)
		for _, o := range objs {
			if err := pw.WriteObject(o); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pw.Close(); err != nil {
			t.Fatal(err)
		}
		dur := time.Since(start)
		t.Logf("window %3d: %9s  %5.1fx vs raw  %5.2fx vs git  packed in %v",
			window, humanBytes(int64(buf.Len())),
			float64(raw)/float64(buf.Len()),
			float64(buf.Len())/float64(gitPackSize),
			dur.Round(time.Millisecond))
	}
}
