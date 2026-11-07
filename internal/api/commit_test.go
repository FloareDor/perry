package api_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/store"
)

func TestCommitWithoutCheckout(t *testing.T) {
	ts, s := fixture(t)
	ctx := context.Background()

	res, err := api.Commit(ctx, s, api.CommitRequest{
		Branch:  "main",
		Message: "edit through the API",
		Author:  "Agent", Email: "agent@example.com",
		Changes: []api.Change{
			{Path: "src/pkg/util.go", Content: []byte("package pkg\n\nfunc Helper() int {\n\treturn 99\n}\n")},
			{Path: "src/new/deep/file.txt", Content: []byte("created in a new directory\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent == "" {
		t.Error("commit has no parent but main existed")
	}

	got, err := api.ReadFile(ctx, s, "main", "src/pkg/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "return 99") {
		t.Errorf("edit did not land: %q", got)
	}
	deep, err := api.ReadFile(ctx, s, "main", "src/new/deep/file.txt")
	if err != nil {
		t.Fatalf("new nested file missing: %v", err)
	}
	if string(deep) != "created in a new directory\n" {
		t.Errorf("nested content wrong: %q", deep)
	}

	// Untouched files must survive the tree rebuild.
	readme, err := api.ReadFile(ctx, s, "main", "README.md")
	if err != nil {
		t.Fatalf("untouched file vanished: %v", err)
	}
	if !strings.Contains(string(readme), "# Demo") {
		t.Errorf("untouched file changed: %q", readme)
	}

	// git must accept the commit this built.
	dst := t.TempDir()
	git(t, dst, "clone", "-q", ts.URL+"/demo.git", "clone")
	cloneDir := filepath.Join(dst, "clone")
	git(t, cloneDir, "fsck", "--strict")

	onDisk, err := os.ReadFile(filepath.Join(cloneDir, "src", "pkg", "util.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "return 99") {
		t.Errorf("cloned file does not have the edit: %q", onDisk)
	}
	if head := git(t, cloneDir, "rev-parse", "HEAD"); head != res.Commit {
		t.Errorf("cloned HEAD %s, commit reported %s", head, res.Commit)
	}
	if msg := git(t, cloneDir, "log", "-1", "--format=%s"); msg != "edit through the API" {
		t.Errorf("commit message is %q", msg)
	}
	if author := git(t, cloneDir, "log", "-1", "--format=%an <%ae>"); author != "Agent <agent@example.com>" {
		t.Errorf("author is %q", author)
	}
}

func TestCommitDelete(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()

	if _, err := api.Commit(ctx, s, api.CommitRequest{
		Branch: "main", Message: "remove a file",
		Changes: []api.Change{{Path: "docs/guide.md", Delete: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ReadFile(ctx, s, "main", "docs/guide.md"); err == nil {
		t.Error("deleted file is still readable")
	}
	// docs/ held only that file, so the directory must be gone too. Git has
	// no empty directories.
	files, err := api.ListFiles(ctx, s, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f, "docs/") {
			t.Errorf("docs/ still has %s", f)
		}
	}
}

func TestCommitOnNewBranch(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()

	res, err := api.Commit(ctx, s, api.CommitRequest{
		Branch: "agent-work", Message: "first commit on a fresh branch",
		Changes: []api.Change{{Path: "only.txt", Content: []byte("alone\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Parent != "" {
		t.Errorf("a branch that did not exist produced a parent: %s", res.Parent)
	}
	if _, err := s.GetRef(ctx, "refs/heads/agent-work"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitExpectGuardsAgainstRaces(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()

	ref, err := s.GetRef(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	stale := ref.Target.String()

	// Someone else commits first.
	if _, err := api.Commit(ctx, s, api.CommitRequest{
		Branch: "main", Message: "the other writer",
		Changes: []api.Change{{Path: "other.txt", Content: []byte("theirs\n")}},
	}); err != nil {
		t.Fatal(err)
	}

	// Our commit, pinned to what we read before, must lose.
	_, err = api.Commit(ctx, s, api.CommitRequest{
		Branch: "main", Message: "ours", Expect: stale,
		Changes: []api.Change{{Path: "ours.txt", Content: []byte("mine\n")}},
	})
	if err == nil {
		t.Fatal("a commit pinned to a stale parent succeeded")
	}
	var conflict *store.ErrRefConflict
	if !asConflict(err, &conflict) {
		t.Fatalf("want a ref conflict, got %v", err)
	}
}

func TestCommitRejectsNoOpAndBadPaths(t *testing.T) {
	_, s := fixture(t)
	ctx := context.Background()

	existing, err := api.ReadFile(ctx, s, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	// Writing a file back unchanged produces the same tree, which would be
	// an empty commit.
	if _, err := api.Commit(ctx, s, api.CommitRequest{
		Branch: "main", Message: "no change",
		Changes: []api.Change{{Path: "README.md", Content: existing}},
	}); err == nil {
		t.Error("a commit with no tree change succeeded")
	}

	// A leading slash is treated as repository-root relative, matching the
	// read endpoints, so only traversal is rejected.
	for _, bad := range []string{"../escape.txt", "..", "a/../../b"} {
		if _, err := api.Commit(ctx, s, api.CommitRequest{
			Branch: "main", Message: "bad path",
			Changes: []api.Change{{Path: bad, Content: []byte("x")}},
		}); err == nil {
			t.Errorf("path %q was accepted", bad)
		}
	}
}
