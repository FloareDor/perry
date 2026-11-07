package bench

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/server"
	"github.com/perry-dev/perry/internal/store"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

func gitEnvB() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=Perry", "GIT_AUTHOR_EMAIL=perry@example.com",
		"GIT_COMMITTER_NAME=Perry", "GIT_COMMITTER_EMAIL=perry@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
	)
}

func gitB(t testing.TB, dir string, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnvB()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// benchServer starts a SQLite-backed perry and pushes a repository of the
// requested size into it.
func benchServer(t testing.TB, files, linesPerFile int) (*httptest.Server, store.Store, int64) {
	t.Helper()
	dir := t.TempDir()

	var mu sync.Mutex
	stores := map[string]store.Store{}
	open := func(name string) (store.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := stores[name]; ok {
			return s, nil
		}
		s, err := sqlite.Open(filepath.Join(dir, name+".db"))
		if err != nil {
			return nil, err
		}
		stores[name] = s
		return s, nil
	}

	gitSrv := server.New(open)
	gitSrv.Logger = nil
	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", &api.Handler{Open: open})
	mux.Handle("/", gitSrv)

	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		for _, s := range stores {
			_ = s.Close()
		}
	})

	// Build a repository with enough content that a clone is not free.
	src := t.TempDir()
	gitB(t, src, "init", "-q", "-b", "main")
	gitB(t, src, "config", "user.email", "perry@example.com")
	gitB(t, src, "config", "user.name", "Perry")

	var total int64
	for i := 0; i < files; i++ {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("package pkg%02d\n\n", i%20))
		for j := 0; j < linesPerFile; j++ {
			sb.WriteString(fmt.Sprintf("// file %d line %d: the quick brown fox jumps over the lazy dog\n", i, j))
		}
		// One distinctive line, so the grep benchmark has a needle.
		if i == files/2 {
			sb.WriteString("func TheNeedleFunction() int { return 1 }\n")
		}
		sb.WriteString(fmt.Sprintf("func Handler%d() {}\n", i))

		rel := filepath.Join(fmt.Sprintf("pkg%02d", i%20), fmt.Sprintf("file%03d.go", i))
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		content := sb.String()
		total += int64(len(content))
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitB(t, src, "add", "-A")
	gitB(t, src, "commit", "-q", "-m", "corpus")
	gitB(t, src, "remote", "add", "perry", ts.URL+"/bench.git")
	gitB(t, src, "push", "-q", "perry", "main")

	s, err := open("bench")
	if err != nil {
		t.Fatal(err)
	}
	return ts, s, total
}

// TestAgentSetupCloneVsAPI is the headline measurement: what it costs an
// agent to start working, cloning the repository versus reading only what
// it needs through the API.
func TestAgentSetupCloneVsAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	ts, s, corpusBytes := benchServer(t, 400, 300)
	ctx := context.Background()

	t.Logf("corpus: 400 files, %s of source", humanBytes(corpusBytes))

	// Path one: a full clone, which is what an agent does today.
	cloneDir := t.TempDir()
	start := time.Now()
	gitB(t, cloneDir, "clone", "-q", ts.URL+"/bench.git", "work")
	cloneTime := time.Since(start)

	var cloneBytes int64
	_ = filepath.Walk(filepath.Join(cloneDir, "work"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			cloneBytes += info.Size()
		}
		return nil
	})

	// Path two: find the file, then read it. This is the whole setup an
	// agent needs before it can make a change.
	start = time.Now()
	res, err := api.Grep(ctx, s, api.GrepOptions{Pattern: "TheNeedleFunction", Rev: "main", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) == 0 {
		t.Fatal("the needle was not found")
	}
	content, err := api.ReadFile(ctx, s, "main", res.Matches[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	apiTime := time.Since(start)

	// And over HTTP, which is what a remote agent actually pays.
	start = time.Now()
	resp, err := http.Get(ts.URL + "/v1/repos/bench/grep?q=TheNeedleFunction&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	resp2, err := http.Get(ts.URL + "/v1/repos/bench/file?ref=main&path=" + res.Matches[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	httpBytes, _ := io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	httpTime := time.Since(start)

	t.Logf("clone:          %8v   %10s on disk", cloneTime.Round(time.Millisecond), humanBytes(cloneBytes))
	t.Logf("API, in process:%8v   %10s read", apiTime.Round(time.Microsecond), humanBytes(int64(len(content))))
	t.Logf("API, over HTTP: %8v   %10s transferred", httpTime.Round(time.Microsecond), humanBytes(httpBytes))
	t.Logf("speedup vs clone: %.0fx in process, %.0fx over HTTP",
		float64(cloneTime)/float64(apiTime), float64(cloneTime)/float64(httpTime))
	t.Logf("bytes moved: %.0fx less over HTTP", float64(cloneBytes)/float64(httpBytes))

	if httpTime > cloneTime {
		t.Errorf("the API path was slower than cloning: %v vs %v", httpTime, cloneTime)
	}
}

// TestForkCost measures branching a repository for a new agent.
func TestForkCost(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	_, s, _ := benchServer(t, 200, 200)
	ctx := context.Background()

	before, _ := s.Count(ctx)
	const forks = 50
	start := time.Now()
	for i := 0; i < forks; i++ {
		if _, err := api.Fork(ctx, s, "main", fmt.Sprintf("agent-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	after, _ := s.Count(ctx)

	t.Logf("%d forks in %v, %v each", forks, elapsed.Round(time.Millisecond), (elapsed / forks).Round(time.Microsecond))
	t.Logf("objects before %d, after %d: %d copied", before, after, after-before)
	if after != before {
		t.Errorf("forking copied %d objects; it should copy none", after-before)
	}
}

// TestConcurrentPushThroughput measures pushes from many agents at once
// against the SQLite backend, which is where reference contention would
// show up if the design were wrong.
func TestConcurrentPushThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	ts, s, _ := benchServer(t, 60, 100)
	ctx := context.Background()

	const agents = 40
	dirs := make([]string, agents)
	for i := 0; i < agents; i++ {
		d := t.TempDir()
		gitB(t, d, "clone", "-q", ts.URL+"/bench.git", "work")
		dirs[i] = filepath.Join(d, "work")

		branch := fmt.Sprintf("agent-%03d", i)
		gitB(t, dirs[i], "checkout", "-q", "-b", branch)
		body := strings.Repeat(fmt.Sprintf("// change by %s\n", branch), 200)
		if err := os.WriteFile(filepath.Join(dirs[i], "change.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		gitB(t, dirs[i], "add", "-A")
		gitB(t, dirs[i], "commit", "-q", "-m", "work from "+branch)
	}

	latencies := make([]time.Duration, agents)
	errs := make([]error, agents)
	outs := make([]string, agents)

	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			begin := time.Now()
			cmd := exec.Command("git", "push", "origin", fmt.Sprintf("agent-%03d", i))
			cmd.Dir = dirs[i]
			cmd.Env = gitEnvB()
			out, err := cmd.CombinedOutput()
			latencies[i] = time.Since(begin)
			errs[i], outs[i] = err, string(out)
		}(i)
	}

	wallStart := time.Now()
	close(release)
	wg.Wait()
	wall := time.Since(wallStart)

	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			t.Errorf("agent %03d: %v\n%s", i, err, outs[i])
		}
	}

	sorted := append([]time.Duration{}, latencies...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	p50 := sorted[len(sorted)/2]
	p99 := sorted[(len(sorted)*99)/100]

	t.Logf("%d/%d concurrent pushes succeeded", agents-failed, agents)
	t.Logf("wall clock %v, p50 %v, p99 %v, max %v",
		wall.Round(time.Millisecond), p50.Round(time.Millisecond),
		p99.Round(time.Millisecond), sorted[len(sorted)-1].Round(time.Millisecond))

	// Every branch must exist exactly once, with a distinct tip.
	refs, err := s.ListRefs(ctx, "refs/heads/agent-")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != agents {
		t.Errorf("%d agent branches on the server, want %d", len(refs), agents)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r.Target.String()] {
			t.Errorf("two branches share tip %s", r.Target)
		}
		seen[r.Target.String()] = true
	}
}

// TestGrepLatency measures a cold repository search.
func TestGrepLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	_, s, corpus := benchServer(t, 400, 300)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		pattern string
		limit   int
	}{
		{"rare literal", "TheNeedleFunction", 10},
		{"common literal", "quick brown fox", 50},
		{"regex", `func Handler\d+\(\)`, 50},
	} {
		start := time.Now()
		res, err := api.Grep(ctx, s, api.GrepOptions{Pattern: tc.pattern, Rev: "main", Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%-16s %6v  %3d matches, %d files scanned",
			tc.name, time.Since(start).Round(time.Microsecond), len(res.Matches), res.Scanned)
	}
	t.Logf("corpus was %s across 400 files", humanBytes(corpus))
}
