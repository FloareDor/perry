package api_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FloareDor/perry/internal/api"
	"github.com/FloareDor/perry/internal/store/sqlite"
)

func seedFiles(t *testing.T, files map[string][]byte) (*sqlite.Store, string) {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "grep.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var changes []api.Change
	for p, c := range files {
		changes = append(changes, api.Change{Path: p, Content: c})
	}
	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "seed", Changes: changes,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, res.Commit
}

// The commit a search ran against is what an agent must pin its write to.
// Resolving the branch again later would read whatever it has moved to.
func TestGrepReportsTheCommitItSearched(t *testing.T) {
	s, tip := seedFiles(t, map[string][]byte{
		"a.go": []byte("package a\n\nconst Timeout = 30\n"),
	})

	res, err := api.Grep(context.Background(), s, api.GrepOptions{Pattern: "Timeout", Rev: "main"})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if res.Commit != tip {
		t.Fatalf("grep searched %s, branch tip is %s", res.Commit, tip)
	}

	// And it must keep reporting the commit it actually read, not the
	// branch, once the branch has moved on.
	moved, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "move", Expect: tip,
		Changes: []api.Change{{Path: "b.go", Content: []byte("package b\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err = api.Grep(context.Background(), s, api.GrepOptions{Pattern: "Timeout", Rev: tip})
	if err != nil {
		t.Fatalf("grep at a pinned commit: %v", err)
	}
	if res.Commit != tip {
		t.Fatalf("grep at %s reported %s (branch is now %s)", tip, res.Commit, moved.Commit)
	}
}

func TestGrepCanReturnTheMatchedFilesWhole(t *testing.T) {
	body := []byte("package a\n\nconst Timeout = 30\n\nfunc F() {}\n")
	s, _ := seedFiles(t, map[string][]byte{
		"a.go":    body,
		"else.go": []byte("package b\n\nconst Other = 1\n"),
	})

	res, err := api.Grep(context.Background(), s, api.GrepOptions{
		Pattern: "Timeout", Rev: "main", IncludeFiles: true,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(res.Files))
	}
	if res.Files[0].Path != "a.go" {
		t.Errorf("returned %s", res.Files[0].Path)
	}
	// Byte-for-byte, because the agent quotes this back as an edit's old
	// text. A transformation here would make every edit miss.
	if res.Files[0].Content != string(body) {
		t.Errorf("content is not verbatim:\n%q\nwant\n%q", res.Files[0].Content, body)
	}

	// Without the flag the response stays matches-only.
	res, err = api.Grep(context.Background(), s, api.GrepOptions{Pattern: "Timeout", Rev: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 {
		t.Errorf("files came back without being asked for")
	}
}

// A search that matched much of the repository must not answer with much of
// the repository.
func TestGrepStopsAtThreeFiles(t *testing.T) {
	files := map[string][]byte{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".go"] = []byte("package " + n + "\n\nconst Timeout = 30\n")
	}
	s, _ := seedFiles(t, files)

	res, err := api.Grep(context.Background(), s, api.GrepOptions{
		Pattern: "Timeout", Rev: "main", IncludeFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 5 {
		t.Fatalf("expected 5 matching files, got %d", res.Matched)
	}
	if len(res.Files) != 3 {
		t.Fatalf("got %d files, want the cap of 3", len(res.Files))
	}
	// The ones returned are the first by match order, so the agent can tell
	// which it has.
	for i, want := range []string{"a.go", "b.go", "c.go"} {
		if res.Files[i].Path != want {
			t.Errorf("file %d is %s, want %s", i, res.Files[i].Path, want)
		}
	}
}

// Returning a binary blob in a text field would be useless at best, so a
// match inside one is reported without the body.
func TestGrepDoesNotReturnBinaryFilesWhole(t *testing.T) {
	bin := append([]byte("Timeout\x00"), bytes.Repeat([]byte{0x00, 0x01}, 64)...)
	s, _ := seedFiles(t, map[string][]byte{"blob.bin": bin})

	res, err := api.Grep(context.Background(), s, api.GrepOptions{
		Pattern: "Timeout", Rev: "main", IncludeFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if f.Path == "blob.bin" {
			t.Fatal("a binary file was returned whole")
		}
	}
}

// The point of the flag is that the file it returns is good enough to edit
// from, in one round trip rather than two.
func TestGrepThenCommitNeedsNoOtherCall(t *testing.T) {
	body := []byte("package a\n\nconst Timeout = 30\n")
	s, _ := seedFiles(t, map[string][]byte{"a.go": body})

	found, err := api.Grep(context.Background(), s, api.GrepOptions{
		Pattern: "Timeout", Rev: "main", IncludeFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Files) != 1 || found.Commit == "" {
		t.Fatalf("grep gave %d files and commit %q", len(found.Files), found.Commit)
	}

	// Everything the commit needs came from that one response.
	old := "const Timeout = 30"
	if !strings.Contains(found.Files[0].Content, old) {
		t.Fatalf("the returned file does not contain the line to edit")
	}
	res, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "raise it", Expect: found.Commit,
		Changes: []api.Change{{
			Path:  found.Files[0].Path,
			Edits: []api.Edit{{Old: old, New: "const Timeout = 90"}},
		}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := api.ReadFile(context.Background(), s, res.Commit, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Timeout = 90") {
		t.Fatalf("file after commit: %q", got)
	}
}

func TestReadFileRange(t *testing.T) {
	body := "one\ntwo\nthree\nfour\nfive\n"
	s, _ := seedFiles(t, map[string][]byte{"f.txt": []byte(body)})
	ctx := context.Background()

	cases := []struct {
		name       string
		start, end int
		want       string
	}{
		{"whole file", 0, 0, body},
		{"a middle range", 2, 4, "two\nthree\nfour\n"},
		{"one line", 3, 3, "three\n"},
		{"from a line to the end", 4, 0, "four\nfive\n"},
		{"the tail, counting back", -2, 0, "four\nfive\n"},
		{"the whole tail is the whole file", -99, 0, body},
		{"an end past the end is clamped", 4, 99, "four\nfive\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := api.ReadFileRange(ctx, s, "main", "f.txt", tc.start, tc.end)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if total != 5 {
				t.Errorf("reported %d lines, want 5", total)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A range nobody can satisfy has to say so rather than return nothing, which
// a model would read as an empty file.
func TestReadFileRangeRejectsAnImpossibleRange(t *testing.T) {
	s, _ := seedFiles(t, map[string][]byte{"f.txt": []byte("one\ntwo\n")})
	ctx := context.Background()

	if _, _, err := api.ReadFileRange(ctx, s, "main", "f.txt", 9, 0); err == nil {
		t.Error("a start past the end was accepted")
	}
	if _, _, err := api.ReadFileRange(ctx, s, "main", "f.txt", 2, 1); err == nil {
		t.Error("a backwards range was accepted")
	}
}

// A file with no trailing newline must not lose its last line, and must not
// be reported as having one more line than it has.
func TestReadFileRangeCountsLinesWithoutATrailingNewline(t *testing.T) {
	s, _ := seedFiles(t, map[string][]byte{"f.txt": []byte("one\ntwo")})

	got, total, err := api.ReadFileRange(context.Background(), s, "main", "f.txt", -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("reported %d lines, want 2", total)
	}
	if string(got) != "two\n" {
		t.Errorf("got %q, want the last line", got)
	}
}

// Truncated must mean "there is more to find". Deriving it from how many
// files were scanned made every repository containing a binary report a
// truncated search, which tells an agent to narrow a search that already
// covered everything.
func TestABinaryFileDoesNotLookLikeTruncation(t *testing.T) {
	s, _ := seedFiles(t, map[string][]byte{
		"a.go":     []byte("package a\n\nconst Timeout = 30\n"),
		"logo.png": append([]byte{0x89, 'P', 'N', 'G', 0x00}, bytes.Repeat([]byte{0x00, 0xff}, 200)...),
	})

	res, err := api.Grep(context.Background(), s, api.GrepOptions{Pattern: "Timeout", Rev: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated {
		t.Error("an exhaustive search reported itself truncated")
	}
	if res.Skipped != 1 {
		t.Errorf("reported %d skipped files, want 1 (the binary)", res.Skipped)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned %d files, want 1", res.Scanned)
	}
}

// And a search that really did stop early must still say so.
func TestAnEarlyStopIsReportedAsTruncated(t *testing.T) {
	files := map[string][]byte{}
	for i := range 40 {
		files[fmt.Sprintf("f%02d.go", i)] = []byte("package p\n\nconst Timeout = 30\n")
	}
	s, _ := seedFiles(t, files)

	res, err := api.Grep(context.Background(), s, api.GrepOptions{
		Pattern: "Timeout", Rev: "main", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("a search that stopped at its limit did not report truncation")
	}
	if len(res.Matches) > 2 {
		t.Errorf("returned %d matches, want at most the limit of 2", len(res.Matches))
	}
}
