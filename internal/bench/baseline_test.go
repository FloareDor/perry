package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/server"
	"github.com/perry-dev/perry/internal/store"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// Real corpora, with the history that comes with them.
//
// A generated corpus makes the two baselines that matter meaningless: with one
// commit, --depth 1 is the whole repository, and 400 files of repeated filler
// compress and grep nothing like source. Everything here runs against a real
// repository cloned at full depth.
//
// Fetch them with:
//
//	git clone --bare https://github.com/spf13/cobra  testdata/cobra-full.git
//	git clone --bare https://github.com/grpc/grpc-go testdata/grpc-go.git
//
// Each repository is served three ways:
//
//	perry   a perry server with the repository pushed into it
//	origin  the bare repository on disk, reachable over file://
//	warm    a full clone already on disk, for the worktree baseline
//
// file:// is a faster transport than HTTP, so every git baseline is measured
// on friendlier ground than perry. That is deliberate: a win against file://
// is a win against a real remote.
// ---------------------------------------------------------------------------

type repoSpec struct {
	dir    string // under testdata/
	label  string
	needle string // a literal that appears in exactly one or two files
	sparse string // top-level directory holding the needle
}

var realRepos = []repoSpec{
	{dir: "cobra-full.git", label: "cobra", needle: "GenMarkdownTree", sparse: "doc"},
	{dir: "grpc-go.git", label: "grpc-go", needle: "purgeDataCache", sparse: "balancer"},
}

type corpus struct {
	spec    repoSpec
	ts      *httptest.Server
	store   store.Store
	origin  string // bare repository, for file:// clones
	warm    string // a clone already on disk
	commits int
	files   int
	packed  int64         // size of the bare repository's pack
	ingest  time.Duration // what it cost to push the repository into perry
}

func fileURL(p string) string {
	q := filepath.ToSlash(p)
	if !strings.HasPrefix(q, "/") {
		q = "/" + q
	}
	return "file://" + q
}

// requireBench gates every benchmark in this package. Together they run for
// about ten minutes, past the default `go test` timeout, so a plain
// `go test ./...` must not run them. Nothing here asserts a correctness
// property that is not also covered in internal/api, internal/pack or
// internal/server, so skipping them loses measurements, not coverage.
func requireBench(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow")
	}
	if os.Getenv("PERRY_BENCH") == "" {
		t.Skip("benchmarks are opt in. Run them with:\n" +
			"\tmake bench      (or: PERRY_BENCH=1 go test ./internal/bench -v -timeout 40m)")
	}
}

// testdataRepo returns the absolute path of a bare corpus, or skips.
func testdataRepo(t testing.TB, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/bench -> repository root
	root := filepath.Join(wd, "..", "..")
	p := filepath.Join(root, "testdata", name)
	if _, err := os.Stat(filepath.Join(p, "HEAD")); err != nil {
		t.Skipf("corpus %s is not in testdata/. Fetch it with:\n"+
			"\tmake corpora      (or: sh scripts/corpora.sh)", name)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func buildRealCorpus(t testing.TB, spec repoSpec) *corpus {
	t.Helper()
	upstream := testdataRepo(t, spec.dir)

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

	// A private bare copy, so the test never writes to the cached corpus.
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	gitB(t, base, "clone", "-q", "--bare", upstream, "origin.git")
	gitB(t, origin, "config", "uploadpack.allowFilter", "true")
	gitB(t, origin, "config", "uploadpack.allowAnySHA1InWant", "true")

	// Whatever the upstream default branch is called, perry serves it as main.
	headRef := gitB(t, origin, "symbolic-ref", "HEAD")
	branch := strings.TrimPrefix(headRef, "refs/heads/")

	start := time.Now()
	gitB(t, origin, "push", "-q", ts.URL+"/bench.git", branch+":refs/heads/main")
	ingest := time.Since(start)

	commits, _ := strconv.Atoi(gitB(t, origin, "rev-list", "--count", branch))
	files := len(strings.Split(gitB(t, origin, "ls-tree", "-r", "--name-only", branch), "\n"))

	// A clone already on disk, for the warm-worktree baseline.
	warmParent := t.TempDir()
	gitB(t, warmParent, "clone", "-q", fileURL(origin), "warm")
	warm := filepath.Join(warmParent, "warm")

	s, err := open("bench")
	if err != nil {
		t.Fatal(err)
	}
	return &corpus{
		spec: spec, ts: ts, store: s, origin: origin, warm: warm,
		commits: commits, files: files,
		packed: packBytes(t, origin), ingest: ingest,
	}
}

// packBytes reports what git materialised in a repository: pack bytes plus
// loose object bytes. For a fresh clone that is the pack it received, and for
// a partial clone it includes whatever the promisor fetched afterwards.
func packBytes(t testing.TB, repo string) int64 {
	t.Helper()
	out := gitB(t, repo, "count-objects", "-v")
	var total int64
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 2 {
			continue
		}
		if f[0] == "size-pack:" || f[0] == "size:" {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				total += n * 1024 // git reports KiB
			}
		}
	}
	return total
}

func treeBytes(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

type setupResult struct {
	name     string
	dur      time.Duration
	wire     int64 // bytes git materialised, or bytes over HTTP
	disk     int64 // total footprint left behind
	note     string
	complete bool // did the path actually produce the needle's content?
}

// TestSetupPathsCompared measures every realistic way an agent can reach one
// file. The task is identical across paths: locate the file containing the
// needle and have its contents in hand, which is the minimum before an agent
// can edit anything.
func TestSetupPathsCompared(t *testing.T) {
	requireBench(t)
	for _, spec := range realRepos {
		t.Run(spec.label, func(t *testing.T) {
			runSetupPaths(t, spec)
		})
	}
}

func runSetupPaths(t *testing.T, spec repoSpec) {
	c := buildRealCorpus(t, spec)
	var results []setupResult

	// grepAndRead is the agent's task: find the needle, read that file.
	// pathspec, when set, is the directory the agent already knows about.
	grepAndRead := func(work string, pathspec string) bool {
		args := []string{"-C", work, "grep", "-n", spec.needle}
		if pathspec != "" {
			args = append(args, "--", pathspec)
		}
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnvB()
		out, err := cmd.CombinedOutput()
		if err != nil || len(out) == 0 {
			return false
		}
		first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
		path := strings.SplitN(first, ":", 2)[0]
		if _, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(path))); err != nil {
			return false
		}
		return true
	}

	// Full clone, which is what an agent does today.
	{
		d := t.TempDir()
		start := time.Now()
		gitB(t, d, "clone", "-q", fileURL(c.origin), "work")
		w := filepath.Join(d, "work")
		ok := grepAndRead(w, "")
		results = append(results, setupResult{
			name: "full clone + git grep", dur: time.Since(start),
			wire: packBytes(t, w), disk: treeBytes(w), complete: ok,
			note: "what an agent does today",
		})
	}

	// Shallow clone, depth 1. With real history this is a real saving.
	{
		d := t.TempDir()
		start := time.Now()
		gitB(t, d, "clone", "-q", "--depth", "1", fileURL(c.origin), "work")
		w := filepath.Join(d, "work")
		ok := grepAndRead(w, "")
		results = append(results, setupResult{
			name: "shallow clone --depth 1", dur: time.Since(start),
			wire: packBytes(t, w), disk: treeBytes(w), complete: ok,
			note: "typical ephemeral agent setup",
		})
	}

	// Shallow and treeless: the cheapest checkout git can produce.
	{
		d := t.TempDir()
		start := time.Now()
		gitB(t, d, "clone", "-q", "--depth", "1", "--filter=blob:none", fileURL(c.origin), "work")
		w := filepath.Join(d, "work")
		ok := grepAndRead(w, "")
		results = append(results, setupResult{
			name: "shallow + blob:none + grep", dur: time.Since(start),
			wire: packBytes(t, w), disk: treeBytes(w), complete: ok,
			note: "lazy fetch defeats a blind grep",
		})
	}

	// Partial plus a sparse checkout of the one directory, for an agent that
	// already knows where to look. This is git's best case, and it is given
	// an answer the other paths have to find for themselves.
	{
		d := t.TempDir()
		start := time.Now()
		gitB(t, d, "clone", "-q", "--depth", "1", "--filter=blob:none", "--sparse",
			fileURL(c.origin), "work")
		w := filepath.Join(d, "work")
		gitB(t, w, "sparse-checkout", "set", spec.sparse)
		ok := grepAndRead(w, spec.sparse)
		results = append(results, setupResult{
			name: "shallow + sparse (dir known)", dur: time.Since(start),
			wire: packBytes(t, w), disk: treeBytes(w), complete: ok,
			note: "git's best case; needs the answer first",
		})
	}

	// Warm worktree on a repository already on disk.
	{
		start := time.Now()
		wt := filepath.Join(t.TempDir(), "wt")
		gitB(t, c.warm, "worktree", "add", "-q", "--detach", wt, "HEAD")
		ok := grepAndRead(wt, "")
		results = append(results, setupResult{
			name: "warm worktree (repo on disk)", dur: time.Since(start),
			wire: 0, disk: treeBytes(wt), complete: ok,
			note: "no transfer; needs a prior clone",
		})
	}

	// Perry over HTTP: grep the server, read the one file that matched.
	{
		ctx := context.Background()
		start := time.Now()
		q := c.ts.URL + "/v1/repos/bench/grep?q=" + url.QueryEscape(spec.needle) + "&ref=main&limit=10"
		resp, err := http.Get(q)
		if err != nil {
			t.Fatal(err)
		}
		n1, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		res, err := api.Grep(ctx, c.store, api.GrepOptions{
			Pattern: spec.needle, Rev: "main", Limit: 10,
		})
		if err != nil || len(res.Matches) == 0 {
			t.Fatalf("perry grep found nothing for %q: %v", spec.needle, err)
		}
		resp2, err := http.Get(c.ts.URL + "/v1/repos/bench/file?ref=main&path=" +
			url.QueryEscape(res.Matches[0].Path))
		if err != nil {
			t.Fatal(err)
		}
		n2, _ := io.Copy(io.Discard, resp2.Body)
		resp2.Body.Close()
		results = append(results, setupResult{
			name: "perry API (grep + file)", dur: time.Since(start),
			wire: n1 + n2, disk: 0, complete: true,
			note: "no checkout, nothing on disk",
		})
	}

	t.Logf("corpus %s: %d commits, %d files, %s packed; needle %q in %s/",
		spec.label, c.commits, c.files, humanBytes(c.packed), spec.needle, spec.sparse)
	t.Logf("ingest: pushing the repository into perry took %v (paid once)",
		c.ingest.Round(time.Millisecond))
	t.Logf("%-30s %9s %11s %11s  %s", "path", "time", "wire", "disk", "note")
	for _, r := range results {
		status := ""
		if !r.complete {
			status = "  [FAILED THE TASK]"
		}
		t.Logf("%-30s %9v %11s %11s  %s%s",
			r.name, r.dur.Round(time.Millisecond),
			humanBytes(r.wire), humanBytes(r.disk), r.note, status)
	}
}

// TestConcurrentWriteContention runs N agents committing to the same
// repository at once. Perry commits through the API with a compare-and-swap;
// the git agents each need a checkout and a push.
func TestConcurrentWriteContention(t *testing.T) {
	requireBench(t)
	const agents = 20
	c := buildRealCorpus(t, realRepos[0]) // cobra: real history, quick to clone
	ctx := context.Background()

	head, err := api.Resolve(ctx, c.store, "main")
	if err != nil {
		t.Fatal(err)
	}

	// Perry: fork a branch, commit through the API, no checkout anywhere.
	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, agents)
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			branch := fmt.Sprintf("agent/p-%02d", i)
			if _, err := api.Fork(ctx, c.store, "main", branch); err != nil {
				errs[i] = err
				return
			}
			_, err := api.Commit(ctx, c.store, api.CommitRequest{
				Branch:  branch,
				Message: fmt.Sprintf("p-%02d", i),
				Expect:  head.String(),
				Changes: []api.Change{{Path: "NOTES.md", Content: []byte(fmt.Sprintf("agent %d\n", i))}},
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()
	perryDur := time.Since(start)
	perryOK := 0
	var perryErr error
	for _, e := range errs {
		if e == nil {
			perryOK++
		} else if perryErr == nil {
			perryErr = e
		}
	}

	// Git: each agent clones, commits and pushes a distinct branch.
	start = time.Now()
	var wg2 sync.WaitGroup
	gerrs := make([]error, agents)
	base := t.TempDir()
	for i := 0; i < agents; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			d := filepath.Join(base, fmt.Sprintf("a%02d", i))
			if err := os.MkdirAll(d, 0o755); err != nil {
				gerrs[i] = err
				return
			}
			run := func(dir string, args ...string) error {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = gitEnvB()
				out, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
				}
				return nil
			}
			if err := run(d, "clone", "-q", "--depth", "1", fileURL(c.origin), "work"); err != nil {
				gerrs[i] = err
				return
			}
			w := filepath.Join(d, "work")
			branch := fmt.Sprintf("agent/g-%02d", i)
			if err := run(w, "checkout", "-q", "-b", branch); err != nil {
				gerrs[i] = err
				return
			}
			if err := os.WriteFile(filepath.Join(w, "NOTES.md"), []byte(fmt.Sprintf("agent %d\n", i)), 0o644); err != nil {
				gerrs[i] = err
				return
			}
			if err := run(w, "add", "-A"); err != nil {
				gerrs[i] = err
				return
			}
			if err := run(w, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "-q", "-m", branch); err != nil {
				gerrs[i] = err
				return
			}
			gerrs[i] = run(w, "push", "-q", "origin", branch)
		}(i)
	}
	wg2.Wait()
	gitDur := time.Since(start)
	gitOK := 0
	var gitErr error
	for _, e := range gerrs {
		if e == nil {
			gitOK++
		} else if gitErr == nil {
			gitErr = e
		}
	}

	t.Logf("corpus %s: %d commits. %d agents, each committing one file to its own branch",
		c.spec.label, c.commits, agents)
	t.Logf("perry  fork + API commit, no checkout: %v, %d/%d landed",
		perryDur.Round(time.Millisecond), perryOK, agents)
	t.Logf("git    clone + commit + push:          %v, %d/%d landed",
		gitDur.Round(time.Millisecond), gitOK, agents)
	if perryDur > 0 {
		t.Logf("perry is %.0fx faster end to end", float64(gitDur)/float64(perryDur))
	}
	t.Logf("note: both landed every branch, so this measures setup cost, not contention")
	if perryErr != nil {
		t.Logf("first perry failure: %v", perryErr)
	}
	if gitErr != nil {
		t.Logf("first git failure: %v", gitErr)
	}
}

// ---------------------------------------------------------------------------
// Same-ref contention.
//
// The previous test gives every agent its own branch, so nothing ever
// contends and the result measures setup cost. This one puts every agent on
// one branch, appending its own line to one shared file. Every writer has to
// land, and no writer may lose another's line.
//
// This is the shape of the problem GitHub describes when it talks about
// thousands of agents pushing to one repository.
// ---------------------------------------------------------------------------

const sharedFile = "AGENTS.md"

type contention struct {
	dur      time.Duration
	attempts int64
	landed   int
	lines    int // distinct agent lines present at the end
}

// TestSameRefContention has every agent append to one file on one branch.
func TestSameRefContention(t *testing.T) {
	requireBench(t)
	// One agent is the uncontended baseline. It costs each system exactly one
	// successful write, so the gap at one agent is fixed overhead -- process
	// spawning, working-tree I/O, HTTP round trips -- and anything above that
	// at higher counts is what contention itself costs.
	for _, agents := range []int{1, 5, 20, 50} {
		t.Run(fmt.Sprintf("%dagents", agents), func(t *testing.T) {
			c := buildRealCorpus(t, realRepos[0])
			p := perryContend(t, c, agents)
			h := perryContendHTTP(t, c, agents)
			g := gitContend(t, c, agents)

			row := func(name string, r contention) {
				t.Logf("%-18s %10v  %2d/%d landed, %4d attempts (%.1f each), %d lines survived",
					name, r.dur.Round(time.Millisecond), r.landed, agents, r.attempts,
					float64(r.attempts)/float64(agents), r.lines)
			}
			t.Logf("%d agents, all appending to %s on main", agents, sharedFile)
			row("perry in process", p)
			row("perry over HTTP", h)
			row("git clone+push", g)
			if h.dur > 0 {
				t.Logf("perry over HTTP is %.0fx faster than git", float64(g.dur)/float64(h.dur))
			}

			// The only result that matters: nobody's work was dropped.
			for _, r := range []struct {
				name string
				c    contention
			}{{"perry in process", p}, {"perry over HTTP", h}, {"git", g}} {
				if r.c.lines != agents {
					t.Errorf("%s lost updates: %d of %d lines survived", r.name, r.c.lines, agents)
				}
			}
		})
	}
}

// perryContend runs agents that read the tip, append a line and commit with a
// compare-and-swap, retrying on the losing side of a conflict.
func perryContend(t *testing.T, c *corpus, agents int) contention {
	ctx := context.Background()
	const maxAttempts = 200

	var attempts int64
	var landed int64
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf("agent %03d was here\n", i)
			for n := 0; n < maxAttempts; n++ {
				atomic.AddInt64(&attempts, 1)

				tip, err := api.Resolve(ctx, c.store, "main")
				if err != nil {
					return
				}
				// Read what is there now, so the append builds on the
				// winner's version rather than overwriting it.
				prev, err := api.ReadFile(ctx, c.store, "main", sharedFile)
				if err != nil {
					prev = nil // the file does not exist yet
				}
				_, err = api.Commit(ctx, c.store, api.CommitRequest{
					Branch:  "main",
					Message: fmt.Sprintf("agent %03d", i),
					Expect:  tip.String(),
					Changes: []api.Change{{Path: sharedFile, Content: append(append([]byte{}, prev...), line...)}},
				})
				if err == nil {
					atomic.AddInt64(&landed, 1)
					return
				}
				var conflict *store.ErrRefConflict
				if !errors.As(err, &conflict) {
					t.Errorf("agent %d: %v", i, err)
					return
				}
				// Lost the race. Read the new tip and try again.
			}
			t.Errorf("agent %d gave up after %d attempts", i, maxAttempts)
		}(i)
	}
	wg.Wait()
	dur := time.Since(start)

	final, err := api.ReadFile(ctx, c.store, "main", sharedFile)
	if err != nil {
		t.Fatalf("reading %s back: %v", sharedFile, err)
	}
	return contention{
		dur: dur, attempts: attempts, landed: int(landed),
		lines: countAgentLines(string(final)),
	}
}

// perryContendHTTP is perryContend over the wire. The in-process variant
// measures the design; this one measures what a remote agent actually pays,
// with JSON encoding and an HTTP round trip on every read and every attempt.
func perryContendHTTP(t *testing.T, c *corpus, agents int) contention {
	const maxAttempts = 200
	base := c.ts.URL + "/v1/repos/bench"

	tipOf := func(cl *http.Client) (string, error) {
		resp, err := cl.Get(base + "/refs?prefix=refs/heads/main")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var out struct {
			Refs []struct {
				Name   string `json:"name"`
				Target string `json:"target"`
			} `json:"refs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", err
		}
		for _, r := range out.Refs {
			if r.Name == "refs/heads/main" {
				return r.Target, nil
			}
		}
		return "", fmt.Errorf("main not found")
	}

	readShared := func(cl *http.Client) []byte {
		resp, err := cl.Get(base + "/file?ref=main&path=" + sharedFile)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		b, _ := io.ReadAll(resp.Body)
		return b
	}

	var attempts int64
	var landed int64
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// One client per agent, as separate agents would have.
			cl := &http.Client{}
			line := fmt.Sprintf("agent %03d was here\n", i)
			for n := 0; n < maxAttempts; n++ {
				atomic.AddInt64(&attempts, 1)

				tip, err := tipOf(cl)
				if err != nil {
					return
				}
				prev := readShared(cl)
				body, err := json.Marshal(api.CommitRequest{
					Branch:  "main",
					Message: fmt.Sprintf("agent %03d", i),
					Expect:  tip,
					Changes: []api.Change{{Path: sharedFile, Content: append(append([]byte{}, prev...), line...)}},
				})
				if err != nil {
					return
				}
				resp, err := cl.Post(base+"/commit", "application/json", bytes.NewReader(body))
				if err != nil {
					return
				}
				code := resp.StatusCode
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if code == http.StatusOK {
					atomic.AddInt64(&landed, 1)
					return
				}
				if code != http.StatusConflict {
					t.Errorf("agent %d: HTTP %d", i, code)
					return
				}
				// 409: lost the compare-and-swap, read the new tip and retry.
			}
			t.Errorf("agent %d gave up after %d attempts", i, maxAttempts)
		}(i)
	}
	wg.Wait()
	dur := time.Since(start)

	final, err := api.ReadFile(context.Background(), c.store, "main", sharedFile)
	if err != nil {
		t.Fatalf("reading %s back: %v", sharedFile, err)
	}
	return contention{
		dur: dur, attempts: attempts, landed: int(landed),
		lines: countAgentLines(string(final)),
	}
}

// gitContend runs the same workload the way an agent with a checkout has to:
// clone, append, commit, push, and on a rejected push pull --rebase and retry.
func gitContend(t *testing.T, c *corpus, agents int) contention {
	const maxAttempts = 200
	base := t.TempDir()

	// Clone first, outside the timed section, so this measures the push war
	// rather than the clones. That is generous to git: perry's agents are
	// timed from a cold start.
	works := make([]string, agents)
	for i := 0; i < agents; i++ {
		d := filepath.Join(base, fmt.Sprintf("a%02d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		gitB(t, d, "clone", "-q", "--depth", "1", fileURL(c.origin), "work")
		works[i] = filepath.Join(d, "work")
	}

	run := func(dir string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnvB()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}

	var attempts int64
	var landed int64
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := works[i]
			line := fmt.Sprintf("agent %03d was here\n", i)
			for n := 0; n < maxAttempts; n++ {
				atomic.AddInt64(&attempts, 1)

				// Start from the server's current state.
				if n > 0 {
					if err := run(w, "fetch", "-q", "--depth", "1", "origin", "main"); err != nil {
						continue
					}
					if err := run(w, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
						continue
					}
				}
				prev, _ := os.ReadFile(filepath.Join(w, sharedFile))
				if err := os.WriteFile(filepath.Join(w, sharedFile),
					append(append([]byte{}, prev...), line...), 0o644); err != nil {
					return
				}
				if err := run(w, "add", "-A"); err != nil {
					return
				}
				if err := run(w, "-c", "user.email=a@b.c", "-c", "user.name=a",
					"commit", "-q", "-m", fmt.Sprintf("agent %03d", i)); err != nil {
					return
				}
				if err := run(w, "push", "-q", "origin", "HEAD:main"); err == nil {
					atomic.AddInt64(&landed, 1)
					return
				}
				// Rejected: somebody else moved main. Reset and retry.
			}
			t.Errorf("git agent %d gave up after %d attempts", i, maxAttempts)
		}(i)
	}
	wg.Wait()
	dur := time.Since(start)

	final := gitB(t, c.origin, "show", "main:"+sharedFile)
	return contention{
		dur: dur, attempts: attempts, landed: int(landed),
		lines: countAgentLines(final),
	}
}

// countAgentLines counts distinct "agent NNN was here" lines.
func countAgentLines(s string) int {
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "agent ") && strings.HasSuffix(line, "was here") {
			seen[line] = true
		}
	}
	return len(seen)
}
