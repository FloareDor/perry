package api_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

// seedForPatch puts one small file on a branch and returns the store and the
// branch tip, which is what an editing commit needs to pass as expect.
func seedForPatch(t *testing.T, path, content string) (*sqlite.Store, string) {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "patch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch:  "main",
		Message: "seed",
		Changes: []api.Change{{Path: path, Content: []byte(content)}},
	})
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	return s, res.Commit
}

func readAt(t *testing.T, s *sqlite.Store, rev, path string) string {
	t.Helper()
	b, err := api.ReadFile(context.Background(), s, rev, path)
	if err != nil {
		t.Fatalf("read %s at %s: %v", path, rev, err)
	}
	return string(b)
}

func TestEditReplacesWithoutResendingTheFile(t *testing.T) {
	const before = "package main\n\nfunc main() {\n\ttimeout := 30\n\t_ = timeout\n}\n"
	s, tip := seedForPatch(t, "main.go", before)

	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch:  "main",
		Message: "raise the timeout",
		Expect:  tip,
		Changes: []api.Change{{
			Path:  "main.go",
			Edits: []api.Edit{{Old: "timeout := 30", New: "timeout := 60"}},
		}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := readAt(t, s, res.Commit, "main.go")
	want := strings.Replace(before, "timeout := 30", "timeout := 60", 1)
	if got != want {
		t.Fatalf("file after edit:\n%q\nwant:\n%q", got, want)
	}
}

func TestSeveralEditsComposeInOrder(t *testing.T) {
	s, tip := seedForPatch(t, "cfg.txt", "a=1\nb=2\nc=3\n")

	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "two edits", Expect: tip,
		Changes: []api.Change{{
			Path: "cfg.txt",
			Edits: []api.Edit{
				{Old: "a=1", New: "a=10"},
				{Old: "c=3", New: "c=30"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got, want := readAt(t, s, res.Commit, "cfg.txt"), "a=10\nb=2\nc=30\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An edit that could land in two places is the failure mode whole-file writes
// were protecting against, so it has to be refused rather than guessed at.
func TestAmbiguousEditIsRefused(t *testing.T) {
	s, tip := seedForPatch(t, "dup.txt", "x = 1\ny = 0\nx = 1\n")

	_, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "ambiguous", Expect: tip,
		Changes: []api.Change{{Path: "dup.txt", Edits: []api.Edit{{Old: "x = 1", New: "x = 2"}}}},
	})
	if err == nil {
		t.Fatal("an edit matching twice was accepted")
	}
	if !strings.Contains(err.Error(), "matched 2 times") {
		t.Fatalf("error does not say what went wrong: %v", err)
	}
	// And the branch must not have moved.
	if got := readAt(t, s, "main", "dup.txt"); got != "x = 1\ny = 0\nx = 1\n" {
		t.Fatalf("the file changed despite the error: %q", got)
	}
}

func TestEditAllReplacesEveryOccurrence(t *testing.T) {
	s, tip := seedForPatch(t, "dup.txt", "x = 1\ny = 0\nx = 1\n")

	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "all", Expect: tip,
		Changes: []api.Change{{
			Path:  "dup.txt",
			Edits: []api.Edit{{Old: "x = 1", New: "x = 2", All: true}},
		}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got, want := readAt(t, s, res.Commit, "dup.txt"), "x = 2\ny = 0\nx = 2\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The hint matters more than the refusal: a model that is only told "no
// match" will re-read the file and send the same bytes again.
func TestAMissSaysWhyItMissed(t *testing.T) {
	cases := []struct {
		name, file, old, want string
	}{
		{"line endings", "one\r\ntwo\r\n", "one\ntwo", "line endings"},
		{"indentation", "func f() {\n\t\treturn\n}\n", "func f() {\n  return\n}", "whitespace"},
		{"absent", "hello\n", "goodbye", "character for character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, tip := seedForPatch(t, "f.txt", tc.file)
			_, err := api.Commit(context.Background(), s, api.CommitRequest{
				Branch: "main", Message: "miss", Expect: tip,
				Changes: []api.Change{{Path: "f.txt", Edits: []api.Edit{{Old: tc.old, New: "z"}}}},
			})
			if err == nil {
				t.Fatal("a non-matching edit was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("hint %q missing from: %v", tc.want, err)
			}
		})
	}
}

func TestEditingAMissingFileFails(t *testing.T) {
	s, tip := seedForPatch(t, "present.txt", "here\n")

	_, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "ghost", Expect: tip,
		Changes: []api.Change{{Path: "absent.txt", Edits: []api.Edit{{Old: "a", New: "b"}}}},
	})
	if err == nil {
		t.Fatal("editing a file that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "absent.txt") {
		t.Fatalf("error does not name the path: %v", err)
	}
}

func TestContentAndEditsTogetherAreRefused(t *testing.T) {
	s, tip := seedForPatch(t, "f.txt", "one\n")

	_, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "both", Expect: tip,
		Changes: []api.Change{{
			Path:    "f.txt",
			Content: []byte("two\n"),
			Edits:   []api.Edit{{Old: "one", New: "three"}},
		}},
	})
	if err == nil {
		t.Fatal("a change carrying both content and edits was accepted")
	}
}

// Rewriting one line of a script must not quietly make it non-executable.
func TestEditKeepsTheFileMode(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "mode.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	seed, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "seed",
		Changes: []api.Change{{Path: "run.sh", Content: []byte("#!/bin/sh\nexit 0\n"), Mode: "100755"}},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "edit", Expect: seed.Commit,
		Changes: []api.Change{{Path: "run.sh", Edits: []api.Edit{{Old: "exit 0", New: "exit 1"}}}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	entries, err := api.ListTree(context.Background(), s, res.Commit, "")
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Path == "run.sh" {
			found = true
			if e.Mode != "100755" {
				t.Fatalf("mode after edit is %s, want 100755", e.Mode)
			}
		}
	}
	if !found {
		t.Fatal("run.sh is missing from the tree")
	}
}

// An edit sits on top of whatever the branch holds now, so a stale expect has
// to be rejected before the edits are applied, not after.
func TestEditRespectsExpect(t *testing.T) {
	s, tip := seedForPatch(t, "f.txt", "one\n")

	if _, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "someone else", Expect: tip,
		Changes: []api.Change{{Path: "f.txt", Content: []byte("theirs\n")}},
	}); err != nil {
		t.Fatalf("concurrent commit: %v", err)
	}

	_, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "stale", Expect: tip,
		Changes: []api.Change{{Path: "f.txt", Edits: []api.Edit{{Old: "one", New: "two"}}}},
	})
	if err == nil {
		t.Fatal("a stale expect was accepted")
	}
}
