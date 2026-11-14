package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/store"
)

// gitEnv pins identity and timestamps so commits are reproducible.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=Perry", "GIT_AUTHOR_EMAIL=perry@example.com",
		"GIT_COMMITTER_NAME=Perry", "GIT_COMMITTER_EMAIL=perry@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
	)
}

type gitRunner struct {
	t   *testing.T
	dir string
	bin string
}

func newGit(t *testing.T, dir string) *gitRunner {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	return &gitRunner{t: t, dir: dir, bin: bin}
}

func (g *gitRunner) run(args ...string) string {
	g.t.Helper()
	out, err := g.try(args...)
	if err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (g *gitRunner) try(args ...string) (string, error) {
	cmd := exec.Command(g.bin, args...)
	cmd.Dir = g.dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// newTestServer starts an httptest server backed by in-memory stores.
func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	var mu sync.Mutex
	stores := map[string]store.Store{}

	srv := New(func(name string) (store.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := stores[name]; ok {
			return s, nil
		}
		s := store.NewMemory()
		stores[name] = s
		return s, nil
	})
	srv.Logger = nil // keep test output clean

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
}

// seedRepo builds a local repository with some history and returns its path.
func seedRepo(t *testing.T, commits int) string {
	t.Helper()
	dir := t.TempDir()
	g := newGit(t, dir)
	g.run("init", "-q", "-b", "main")
	g.run("config", "user.email", "perry@example.com")
	g.run("config", "user.name", "Perry")

	for i := 0; i < commits; i++ {
		body := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 200) +
			fmt.Sprintf("revision %d\n", i)
		if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "src", "pkg")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		code := fmt.Sprintf("package pkg\n\n// Rev %d\nconst N = %d\n", i, i)
		if err := os.WriteFile(filepath.Join(sub, "a.go"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		g.run("add", "-A")
		g.run("commit", "-q", "-m", fmt.Sprintf("revision %d", i))
	}
	return dir
}

// TestPushThenClone is the end-to-end check: a real git client pushes to
// perry, and a second real git client clones it back and gets identical
// history.
func TestPushThenClone(t *testing.T) {
	ts, _ := newTestServer(t)

	src := seedRepo(t, 8)
	g := newGit(t, src)
	url := ts.URL + "/demo.git"

	g.run("remote", "add", "perry", url)
	g.run("push", "-q", "perry", "main")

	wantHead := g.run("rev-parse", "main")
	wantLog := g.run("log", "--format=%H %s", "main")

	dst := t.TempDir()
	gc := newGit(t, dst)
	gc.run("clone", "-q", url, "clone")

	cloned := newGit(t, filepath.Join(dst, "clone"))
	gotHead := cloned.run("rev-parse", "HEAD")
	if gotHead != wantHead {
		t.Fatalf("cloned HEAD %s, pushed %s", gotHead, wantHead)
	}
	if got := cloned.run("log", "--format=%H %s", "HEAD"); got != wantLog {
		t.Errorf("history differs\n got:\n%s\nwant:\n%s", got, wantLog)
	}

	// fsck is git's own integrity check over everything it received.
	if out, err := cloned.try("fsck", "--strict"); err != nil {
		t.Errorf("git fsck on the clone failed: %v\n%s", err, out)
	}

	// The working tree must match too, not only the commit graph.
	wantFile, err := os.ReadFile(filepath.Join(src, "src", "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	gotFile, err := os.ReadFile(filepath.Join(dst, "clone", "src", "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotFile) != string(wantFile) {
		t.Error("checked-out file content differs")
	}
}

// TestIncrementalFetch checks the have/want negotiation: a second push
// followed by a fetch must transfer only what is new.
func TestIncrementalFetch(t *testing.T) {
	ts, _ := newTestServer(t)
	url := ts.URL + "/demo.git"

	src := seedRepo(t, 3)
	g := newGit(t, src)
	g.run("remote", "add", "perry", url)
	g.run("push", "-q", "perry", "main")

	dst := t.TempDir()
	newGit(t, dst).run("clone", "-q", url, "clone")
	cloned := newGit(t, filepath.Join(dst, "clone"))

	// Add more history and push it.
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("added later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run("add", "-A")
	g.run("commit", "-q", "-m", "a later commit")
	g.run("push", "-q", "perry", "main")
	want := g.run("rev-parse", "main")

	cloned.run("fetch", "-q", "origin")
	if got := cloned.run("rev-parse", "origin/main"); got != want {
		t.Fatalf("after fetch origin/main is %s, want %s", got, want)
	}
	if out, err := cloned.try("fsck", "--strict"); err != nil {
		t.Errorf("fsck after fetch: %v\n%s", err, out)
	}
}

// TestPushRejectsStaleRef checks that a non-fast-forward push loses the
// compare-and-swap rather than silently overwriting.
func TestPushRejectsStaleRef(t *testing.T) {
	ts, _ := newTestServer(t)
	url := ts.URL + "/demo.git"

	src := seedRepo(t, 2)
	g := newGit(t, src)
	g.run("remote", "add", "perry", url)
	g.run("push", "-q", "perry", "main")

	// A second clone pushes a commit, moving the branch.
	otherDir := t.TempDir()
	newGit(t, otherDir).run("clone", "-q", url, "other")
	other := newGit(t, filepath.Join(otherDir, "other"))
	if err := os.WriteFile(filepath.Join(otherDir, "other", "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other.run("add", "-A")
	other.run("commit", "-q", "-m", "from the other clone")
	other.run("push", "-q", "origin", "main")

	// The first clone, still on the old tip, must be refused.
	if err := os.WriteFile(filepath.Join(src, "y.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.run("add", "-A")
	g.run("commit", "-q", "-m", "stale push")
	out, err := g.try("push", "perry", "main")
	if err == nil {
		t.Fatalf("stale push succeeded; output:\n%s", out)
	}
	t.Logf("stale push was refused: %s", firstLine(out))
}

// TestMultipleBranches checks that independent branches round trip.
func TestMultipleBranches(t *testing.T) {
	ts, _ := newTestServer(t)
	url := ts.URL + "/demo.git"

	src := seedRepo(t, 2)
	g := newGit(t, src)
	g.run("remote", "add", "perry", url)

	for i := 0; i < 5; i++ {
		branch := fmt.Sprintf("agent-%02d", i)
		g.run("checkout", "-q", "-b", branch, "main")
		if err := os.WriteFile(filepath.Join(src, branch+".txt"), []byte(branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		g.run("add", "-A")
		g.run("commit", "-q", "-m", "work on "+branch)
	}
	g.run("push", "-q", "perry", "--all")

	dst := t.TempDir()
	newGit(t, dst).run("clone", "-q", url, "clone")
	cloned := newGit(t, filepath.Join(dst, "clone"))
	out := cloned.run("branch", "-r")
	for i := 0; i < 5; i++ {
		if !strings.Contains(out, fmt.Sprintf("agent-%02d", i)) {
			t.Errorf("branch agent-%02d missing from the clone:\n%s", i, out)
		}
	}
}

// TestDeleteBranch checks the delete path, which sends no packfile.
func TestDeleteBranch(t *testing.T) {
	ts, srv := newTestServer(t)
	url := ts.URL + "/demo.git"

	src := seedRepo(t, 2)
	g := newGit(t, src)
	g.run("remote", "add", "perry", url)
	g.run("checkout", "-q", "-b", "doomed")
	g.run("push", "-q", "perry", "main", "doomed")

	st, err := srv.Repo("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRef(context.Background(), "refs/heads/doomed"); err != nil {
		t.Fatalf("branch was not created: %v", err)
	}

	g.run("push", "-q", "perry", "--delete", "doomed")
	if _, err := st.GetRef(context.Background(), "refs/heads/doomed"); err == nil {
		t.Error("branch still present after delete")
	}
}

// TestEmptyRepoAdvertisement checks that cloning a repository with no
// commits fails cleanly rather than hanging or producing a broken clone.
func TestEmptyRepoAdvertisement(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := t.TempDir()
	g := newGit(t, dir)
	out, err := g.try("clone", ts.URL+"/fresh.git", "fresh")
	if err != nil {
		t.Logf("clone of an empty repo reported: %s", firstLine(out))
	}
	// Either outcome is acceptable; a hang or a corrupt clone is not.
	if _, statErr := os.Stat(filepath.Join(dir, "fresh", ".git")); statErr == nil {
		inner := newGit(t, filepath.Join(dir, "fresh"))
		if fsck, err := inner.try("fsck"); err != nil {
			t.Errorf("empty clone is not a valid repo: %v\n%s", err, fsck)
		}
	}
}

// TestConcurrentPushesToDistinctBranches is the headline concurrency case:
// many agents pushing their own branches at once must all succeed, because
// each one touches a different reference row.
func TestConcurrentPushesToDistinctBranches(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	ts, srv := newTestServer(t)
	url := ts.URL + "/fleet.git"

	// Seed a base commit every agent branches from.
	base := seedRepo(t, 1)
	gb := newGit(t, base)
	gb.run("remote", "add", "perry", url)
	gb.run("push", "-q", "perry", "main")

	const agents = 20
	clones := make([]string, agents)
	for i := 0; i < agents; i++ {
		dir := t.TempDir()
		newGit(t, dir).run("clone", "-q", url, "work")
		clones[i] = filepath.Join(dir, "work")

		g := newGit(t, clones[i])
		branch := fmt.Sprintf("agent-%02d", i)
		g.run("checkout", "-q", "-b", branch)
		if err := os.WriteFile(filepath.Join(clones[i], branch+".txt"), []byte(branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		g.run("add", "-A")
		g.run("commit", "-q", "-m", "work from "+branch)
	}

	// Release all the pushes at once.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, agents)
	outs := make([]string, agents)
	begin := time.Now()

	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cmd := exec.Command("git", "push", "origin", fmt.Sprintf("agent-%02d", i))
			cmd.Dir = clones[i]
			cmd.Env = gitEnv()
			out, err := cmd.CombinedOutput()
			errs[i], outs[i] = err, string(out)
		}(i)
	}
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)

	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			t.Errorf("agent %02d failed: %v\n%s", i, err, outs[i])
		}
	}
	t.Logf("%d/%d concurrent pushes succeeded in %v", agents-failed, agents, elapsed.Round(time.Millisecond))

	// Every branch must be present and distinct.
	st, err := srv.Repo("fleet")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := st.ListRefs(context.Background(), "refs/heads/")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[object.ID]bool{}
	branches := 0
	for _, r := range refs {
		if strings.HasPrefix(r.ShortName(), "agent-") {
			branches++
			if seen[r.Target] {
				t.Errorf("two branches share the tip %s", r.Target)
			}
			seen[r.Target] = true
		}
	}
	if branches != agents {
		t.Errorf("%d agent branches on the server, expected %d", branches, agents)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
