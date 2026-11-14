package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FloareDor/perry/internal/api"
	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/server"
	"github.com/FloareDor/perry/internal/store"
	"github.com/FloareDor/perry/internal/store/sqlite"
)

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=Perry", "GIT_AUTHOR_EMAIL=perry@example.com",
		"GIT_COMMITTER_NAME=Perry", "GIT_COMMITTER_EMAIL=perry@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
	)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture starts a SQLite-backed perry, pushes a small repository into it,
// and returns the server URL and the store.
func fixture(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	dataDir := t.TempDir()

	var mu sync.Mutex
	stores := map[string]store.Store{}
	open := func(name string) (store.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := stores[name]; ok {
			return s, nil
		}
		s, err := sqlite.Open(filepath.Join(dataDir, name+".db"))
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

	// Build a repository with nested directories and recognizable content.
	src := t.TempDir()
	git(t, src, "init", "-q", "-b", "main")
	git(t, src, "config", "user.email", "perry@example.com")
	git(t, src, "config", "user.name", "Perry")

	files := map[string]string{
		"README.md":         "# Demo\n\nA fixture repository.\n",
		"src/main.go":       "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"src/pkg/util.go":   "package pkg\n\nfunc Helper() int {\n\treturn 42\n}\n",
		"src/pkg/util_b.go": "package pkg\n\nfunc Other() string {\n\treturn \"other\"\n}\n",
		"docs/guide.md":     "# Guide\n\nHow to use the demo.\nSecond line here.\n",
		"data/binary.bin":   "\x00\x01\x02binary content here\x00",
	}
	for p, content := range files {
		full := filepath.Join(src, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "initial commit")
	git(t, src, "remote", "add", "perry", ts.URL+"/demo.git")
	git(t, src, "push", "-q", "perry", "main")

	s, err := open("demo")
	if err != nil {
		t.Fatal(err)
	}
	return ts, s
}

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repo.db")

	s1, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	obj := mustObject("hello persistence\n")
	id, err := s1.Put(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.UpdateRefs(ctx, []store.RefUpdate{{Name: "refs/heads/main", New: id}}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, err := s2.Get(ctx, id)
	if err != nil {
		t.Fatalf("object did not survive a reopen: %v", err)
	}
	if string(got.Payload) != "hello persistence\n" {
		t.Errorf("payload changed: %q", got.Payload)
	}
	ref, err := s2.GetRef(ctx, "refs/heads/main")
	if err != nil {
		t.Fatalf("ref did not survive a reopen: %v", err)
	}
	if ref.Target != id {
		t.Errorf("ref points at %s, want %s", ref.Target, id)
	}
}

func TestSQLiteRefCAS(t *testing.T) {
	s, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	a, _ := s.Put(ctx, mustObject("a"))
	b, _ := s.Put(ctx, mustObject("b"))

	// Create: the ref must not exist.
	if err := s.UpdateRefs(ctx, []store.RefUpdate{{Name: "refs/heads/x", New: a}}); err != nil {
		t.Fatal(err)
	}
	// A second create must lose, because the ref now exists.
	err = s.UpdateRefs(ctx, []store.RefUpdate{{Name: "refs/heads/x", New: b}})
	if err == nil {
		t.Fatal("creating an existing ref succeeded")
	}
	var conflict *store.ErrRefConflict
	if !asConflict(err, &conflict) {
		t.Fatalf("want a ref conflict, got %v", err)
	}
	if conflict.Actual != a {
		t.Errorf("conflict reports %s, ref holds %s", conflict.Actual, a)
	}

	// A correct compare-and-swap succeeds.
	if err := s.UpdateRefs(ctx, []store.RefUpdate{{Name: "refs/heads/x", Old: a, New: b}}); err != nil {
		t.Fatal(err)
	}

	// A batch where one update loses must leave nothing applied.
	err = s.UpdateRefs(ctx, []store.RefUpdate{
		{Name: "refs/heads/y", New: a},
		{Name: "refs/heads/x", Old: a, New: a}, // stale, x holds b
	})
	if err == nil {
		t.Fatal("a batch with a stale update succeeded")
	}
	if _, err := s.GetRef(ctx, "refs/heads/y"); err == nil {
		t.Error("the batch was partially applied: y exists")
	}
}

func TestReadFileAndTree(t *testing.T) {
	ts, s := fixture(t)
	ctx := context.Background()

	data, err := api.ReadFile(ctx, s, "main", "src/pkg/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "func Helper()") {
		t.Errorf("unexpected file content: %q", data)
	}

	// HEAD must resolve to the same thing as the branch.
	viaHead, err := api.ReadFile(ctx, s, "HEAD", "src/pkg/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(viaHead) != string(data) {
		t.Error("HEAD and main disagree")
	}

	entries, err := api.ListTree(ctx, s, "main", "src")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Path)
	}
	if len(names) != 2 {
		t.Errorf("src/ listed %v, want main.go and pkg", names)
	}

	// Over HTTP.
	resp, err := http.Get(ts.URL + "/v1/repos/demo/file?ref=main&path=README.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("file endpoint returned %d", resp.StatusCode)
	}
}

func TestReadFileRejectsTraversal(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()
	for _, bad := range []string{"../etc/passwd", "/../../secret", "src/../../out", ".."} {
		if _, err := api.ReadFile(ctx, s, "main", bad); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
}

func TestGrep(t *testing.T) {
	ts, s := fixture(t)
	ctx := context.Background()

	res, err := api.Grep(ctx, s, api.GrepOptions{Pattern: "func ", Rev: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) < 3 {
		t.Errorf("found %d matches for 'func ', expected at least 3", len(res.Matches))
	}

	// A path filter must narrow the search.
	narrowed, err := api.Grep(ctx, s, api.GrepOptions{
		Pattern: "func ", Rev: "main", PathPrefixes: []string{"src/pkg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range narrowed.Matches {
		if !strings.HasPrefix(m.Path, "src/pkg/") {
			t.Errorf("path filter leaked %s", m.Path)
		}
	}
	if len(narrowed.Matches) >= len(res.Matches) {
		t.Error("the path filter did not narrow anything")
	}

	// Binary files must be skipped, not matched as garbage.
	bin, err := api.Grep(ctx, s, api.GrepOptions{Pattern: "binary content", Rev: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bin.Matches) != 0 {
		t.Errorf("matched inside a binary file: %+v", bin.Matches)
	}

	// Case sensitivity.
	lower, _ := api.Grep(ctx, s, api.GrepOptions{Pattern: "HELPER", Rev: "main"})
	upper, _ := api.Grep(ctx, s, api.GrepOptions{Pattern: "HELPER", Rev: "main", CaseSensitive: true})
	if len(lower.Matches) == 0 {
		t.Error("case-insensitive search found nothing")
	}
	if len(upper.Matches) != 0 {
		t.Error("case-sensitive search matched the wrong case")
	}

	// The limit must stop the search and say so.
	limited, err := api.Grep(ctx, s, api.GrepOptions{Pattern: "e", Rev: "main", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Matches) != 2 || !limited.Truncated {
		t.Errorf("limit 2 gave %d matches, truncated=%v", len(limited.Matches), limited.Truncated)
	}

	// Over HTTP.
	resp, err := http.Get(ts.URL + "/v1/repos/demo/grep?q=func+Helper")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out api.GrepResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) == 0 {
		t.Error("the grep endpoint found nothing")
	}
}

func TestListFilesGlob(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()

	all, err := api.ListFiles(ctx, s, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Errorf("listed %d files, want 6: %v", len(all), all)
	}

	goFiles, err := api.ListFiles(ctx, s, "main", "src/**/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(goFiles) != 3 {
		t.Errorf("src/**/*.go matched %v, want 3 files", goFiles)
	}
	for _, p := range goFiles {
		if !strings.HasSuffix(p, ".go") {
			t.Errorf("glob matched a non-Go file: %s", p)
		}
	}
}

func TestForkIsFree(t *testing.T) {
	ts, s := fixture(t)
	ctx := context.Background()

	before, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	target, err := api.Fork(ctx, s, "main", "agent-01")
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	after, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a fork copied %d objects; it should copy none", after-before)
	}
	t.Logf("fork took %v and copied 0 objects", elapsed.Round(time.Microsecond))

	ref, err := s.GetRef(ctx, "refs/heads/agent-01")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Target != target {
		t.Error("the forked branch points somewhere else")
	}

	// Forking onto an existing branch must lose rather than overwrite.
	if _, err := api.Fork(ctx, s, "main", "agent-01"); err == nil {
		t.Error("forking onto an existing branch succeeded")
	}

	// Over HTTP.
	resp, err := http.Post(ts.URL+"/v1/repos/demo/fork", "application/json",
		strings.NewReader(`{"from":"main","branch":"agent-02"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fork endpoint returned %d", resp.StatusCode)
	}
}

func TestAPITokenRequired(t *testing.T) {
	s, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	h := &api.Handler{
		Open:  func(string) (store.Store, error) { return s, nil },
		Token: "secret-token",
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/repos/demo/refs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated request returned %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/v1/repos/demo/refs", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("authenticated request returned %d", resp2.StatusCode)
	}
}

func TestEndToEndCloneFromSQLite(t *testing.T) {
	ts, _ := fixture(t)

	dst := t.TempDir()
	git(t, dst, "clone", "-q", ts.URL+"/demo.git", "clone")
	cloneDir := filepath.Join(dst, "clone")

	got, err := os.ReadFile(filepath.Join(cloneDir, "src", "pkg", "util.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "func Helper()") {
		t.Errorf("cloned file content is wrong: %q", got)
	}
	git(t, cloneDir, "fsck", "--strict")
}

func mustObject(s string) *object.Object {
	return &object.Object{Type: object.TypeBlob, Payload: []byte(s)}
}

func asConflict(err error, target **store.ErrRefConflict) bool {
	return errors.As(err, target)
}
