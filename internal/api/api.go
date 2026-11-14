// Package api serves the read endpoints an agent uses instead of cloning:
// resolve a ref, list a tree, stream one file at one ref, grep the whole
// repository, and fork a branch.
package api

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/perry-dev/perry/internal/object"
	"github.com/perry-dev/perry/internal/store"
)

// Resolve turns a revision into an object name.
//
// It accepts a full name, a branch, a tag, HEAD, or a full 40-character
// hex id. Short hex prefixes are not supported: resolving one means a scan
// of every object, which is exactly the cost this API exists to avoid.
func Resolve(ctx context.Context, s store.Store, rev string) (object.ID, error) {
	if rev == "" {
		rev = "HEAD"
	}

	if id, err := object.ParseID(rev); err == nil {
		if ok, err := s.Has(ctx, id); err == nil && ok {
			return id, nil
		}
		return object.ID{}, fmt.Errorf("%w: object %s", store.ErrNotFound, rev)
	}

	if rev == "HEAD" {
		target, err := s.HeadTarget(ctx)
		if err != nil {
			return object.ID{}, err
		}
		rev = target
	}

	// Try the name as given, then the usual namespaces, in git's own order.
	for _, candidate := range []string{
		rev,
		"refs/" + rev,
		"refs/heads/" + rev,
		"refs/tags/" + rev,
	} {
		ref, err := s.GetRef(ctx, candidate)
		if err == nil {
			return ref.Target, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return object.ID{}, err
		}
	}
	return object.ID{}, fmt.Errorf("%w: revision %q", store.ErrNotFound, rev)
}

// CommitTree returns the tree a revision points at, following a tag to its
// commit and a commit to its tree.
func CommitTree(ctx context.Context, s store.Store, rev string) (object.ID, error) {
	id, err := Resolve(ctx, s, rev)
	if err != nil {
		return object.ID{}, err
	}

	// A tag may point at a tag, so follow the chain rather than one hop.
	for i := 0; i < 10; i++ {
		obj, err := s.Get(ctx, id)
		if err != nil {
			return object.ID{}, err
		}
		switch obj.Type {
		case object.TypeTree:
			return id, nil
		case object.TypeCommit:
			c, err := object.DecodeCommit(obj.Payload)
			if err != nil {
				return object.ID{}, err
			}
			return c.Tree, nil
		case object.TypeTag:
			t, err := object.DecodeTag(obj.Payload)
			if err != nil {
				return object.ID{}, err
			}
			id = t.Object
		default:
			return object.ID{}, fmt.Errorf("api: %s is a %s, not a commit", rev, obj.Type)
		}
	}
	return object.ID{}, fmt.Errorf("api: tag chain from %q is too deep", rev)
}

// Entry is one path in a tree listing.
type Entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	ID   string `json:"id"`
	Size int64  `json:"size,omitempty"`
}

// ListTree returns the entries directly under a directory at a revision.
// An empty dir lists the root.
func ListTree(ctx context.Context, s store.Store, rev, dir string) ([]Entry, error) {
	treeID, err := CommitTree(ctx, s, rev)
	if err != nil {
		return nil, err
	}
	treeID, err = descend(ctx, s, treeID, dir)
	if err != nil {
		return nil, err
	}

	tree, err := readTree(ctx, s, treeID)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		ent := Entry{
			Path: path.Join(dir, e.Name),
			Mode: e.Mode.String(),
			ID:   e.ID.String(),
			Type: entryType(e.Mode),
		}
		// A blob's size needs the object, so only fetch it for files.
		if ent.Type == "blob" {
			if obj, err := s.Get(ctx, e.ID); err == nil {
				ent.Size = obj.Size()
			}
		}
		out = append(out, ent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func entryType(m object.Mode) string {
	switch {
	case m.IsDir():
		return "tree"
	case m.IsSubmodule():
		return "commit"
	default:
		return "blob"
	}
}

// ReadFile returns the contents of one path at one revision.
//
// This is the endpoint that replaces a clone for most agent reads: it walks
// the tree from the root to the named path and fetches exactly the blobs on
// that route, which is one object per path component.
func ReadFile(ctx context.Context, s store.Store, rev, filePath string) ([]byte, error) {
	treeID, err := CommitTree(ctx, s, rev)
	if err != nil {
		return nil, err
	}

	clean := cleanPath(filePath)
	if clean == "" {
		return nil, fmt.Errorf("api: no path given")
	}
	dir, name := path.Split(clean)

	treeID, err = descend(ctx, s, treeID, strings.TrimSuffix(dir, "/"))
	if err != nil {
		return nil, err
	}
	tree, err := readTree(ctx, s, treeID)
	if err != nil {
		return nil, err
	}
	entry, ok := tree.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s at %s", store.ErrNotFound, clean, rev)
	}
	if entry.Mode.IsDir() {
		return nil, fmt.Errorf("api: %s is a directory", clean)
	}

	obj, err := s.Get(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	return obj.Payload, nil
}

// descend walks from a tree down a slash-separated path.
func descend(ctx context.Context, s store.Store, treeID object.ID, dir string) (object.ID, error) {
	dir = cleanPath(dir)
	if dir == "" {
		return treeID, nil
	}
	for _, part := range strings.Split(dir, "/") {
		tree, err := readTree(ctx, s, treeID)
		if err != nil {
			return object.ID{}, err
		}
		entry, ok := tree.Lookup(part)
		if !ok {
			return object.ID{}, fmt.Errorf("%w: directory %s", store.ErrNotFound, dir)
		}
		if !entry.Mode.IsDir() {
			return object.ID{}, fmt.Errorf("api: %s is not a directory", part)
		}
		treeID = entry.ID
	}
	return treeID, nil
}

// cleanPath normalizes a request path and refuses anything that tries to
// escape the repository root.
func cleanPath(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	// path.Clean resolves "." and ".." segments. Anything still pointing
	// above the root after that is a traversal attempt.
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

func readTree(ctx context.Context, s store.Store, id object.ID) (*object.Tree, error) {
	obj, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if obj.Type != object.TypeTree {
		return nil, fmt.Errorf("api: %s is a %s, not a tree", id, obj.Type)
	}
	return object.DecodeTree(obj.Payload)
}

// GrepOptions controls a repository search.
type GrepOptions struct {
	Pattern       string   // a Go regular expression
	Rev           string   // revision to search, default HEAD
	PathPrefixes  []string // restrict to these directories or files
	CaseSensitive bool
	Context       int // lines of context on each side
	Limit         int // stop after this many matches, 0 means 1000
	MaxFileSize   int64
	// IncludeFiles returns the full contents of the matched files along
	// with the matches, when few enough of them matched to be worth it.
	//
	// An agent that greps in order to edit what it finds otherwise spends
	// a second round trip, and a second model turn, fetching a file the
	// server already had in memory. The turn is the expensive half: the
	// model re-reads its whole prompt prefix on every one.
	IncludeFiles bool
}

// Match is one matching line.
type Match struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// GrepFile is one matched file returned whole.
type GrepFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// GrepResult is a search response.
type GrepResult struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
	Scanned   int     `json:"filesScanned"`
	Matched   int     `json:"filesMatched"`
	// Skipped counts files passed over as binary or oversized. They are
	// not failures and not truncation; the caller is told so it can see
	// that the search covered less than the repository holds.
	Skipped int `json:"filesSkipped,omitempty"`
	// Commit is the commit the search ran against. A caller that edits
	// what it found can send this back as a commit request's expect
	// without looking the branch up again, and it is the version the
	// matches actually came from rather than whatever the branch has
	// moved to since.
	Commit string `json:"commit,omitempty"`
	// Files carries the matched files in full when IncludeFiles was set.
	Files []GrepFile `json:"files,omitempty"`
}

// Limits on IncludeFiles. A search that matched half the repository must not
// answer with half the repository: past a few files the agent is still
// narrowing down, and the right next step is another search, not a download.
const (
	maxGrepFiles     = 3
	maxGrepFileBytes = 256 << 10
)

// defaultMaxFileSize skips blobs too large to be source code. A repository
// with a large binary in it should not turn every grep into a scan of it.
const defaultMaxFileSize = 2 << 20

// Grep searches every file at a revision.
//
// The search reads blobs out of the object store directly, so it never
// materializes a working tree.
//
// Files are scanned in parallel. The work is dominated by fetching blobs
// from the store rather than by the regex, so a sequential scan spends
// almost all its time waiting on one read at a time. SQLite in WAL mode
// serves concurrent readers, so fanning out across cores turns that wait
// into throughput.
func Grep(ctx context.Context, s store.Store, opts GrepOptions) (*GrepResult, error) {
	if opts.Pattern == "" {
		return nil, errors.New("api: grep needs a pattern")
	}
	if opts.Limit <= 0 {
		opts.Limit = 1000
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = defaultMaxFileSize
	}

	expr := opts.Pattern
	if !opts.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("api: bad pattern: %w", err)
	}

	// Resolve once and search by commit id. The result reports which
	// commit it searched, so a caller that edits what it found can pin its
	// write to that exact version; resolving the name twice would open a
	// window where the two disagree.
	commitID, err := Resolve(ctx, s, opts.Rev)
	if err != nil {
		return nil, err
	}
	treeID, err := CommitTree(ctx, s, commitID.String())
	if err != nil {
		return nil, err
	}

	// Collect the candidate paths first. Walking the trees is cheap, a
	// handful of reads for a whole repository, and having the full list up
	// front is what allows the scan to be spread across workers.
	type candidate struct {
		path string
		blob object.ID
	}
	var files []candidate
	err = walkTree(ctx, s, treeID, "", func(filePath string, blobID object.ID) error {
		if matchesPrefix(filePath, opts.PathPrefixes) {
			files = append(files, candidate{filePath, blobID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	workers := runtime.NumCPU()
	if workers > len(files) {
		workers = len(files)
	}
	if workers < 1 {
		workers = 1
	}

	// Each worker keeps its own matches and counters, so the hot path takes
	// no locks. Results are merged in path order afterwards, which also
	// makes the output deterministic regardless of how the work was split.
	type shard struct {
		matches []Match
		scanned int
		matched int
		skipped int
	}
	shards := make([]shard, workers)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// found counts matches across all workers so the scan can stop early.
	//
	// The threshold is a multiple of the limit rather than the limit
	// itself. Workers take files in arbitrary order, so stopping at exactly
	// the limit would return whichever matches happened to finish first.
	// Overshooting and then sorting gives the caller the lowest-sorting
	// matches among everything scanned, and Truncated tells them the
	// repository was not searched exhaustively.
	var found int64
	stopAfter := int64(opts.Limit) * 4

	var next int64 = -1
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			sh := &shards[w]
			for {
				i := int(atomic.AddInt64(&next, 1))
				if i >= len(files) {
					return
				}
				if atomic.LoadInt64(&found) >= stopAfter {
					return
				}
				select {
				case <-ctx.Done():
					return
				default:
				}

				f := files[i]
				obj, err := s.Get(ctx, f.blob)
				if err != nil {
					if errors.Is(err, store.ErrNotFound) || ctx.Err() != nil {
						continue
					}
					errCh <- err
					cancel()
					return
				}
				if obj.Size() > opts.MaxFileSize || isBinary(obj.Payload) {
					sh.skipped++
					continue
				}
				sh.scanned++

				// Test the whole file before splitting it into lines. A
				// pattern that appears nowhere then costs one scan of the
				// buffer rather than one regex call per line, and most
				// files do not match most searches. Matching the bytes
				// directly also avoids copying the payload into a string.
				if !re.Match(obj.Payload) {
					continue
				}
				sh.matched++

				before := len(sh.matches)
				lines := strings.Split(string(obj.Payload), "\n")
				for n, line := range lines {
					if !re.MatchString(line) {
						continue
					}
					m := Match{Path: f.path, Line: n + 1, Text: line}
					if opts.Context > 0 {
						m.Before = sliceLines(lines, n-opts.Context, n)
						m.After = sliceLines(lines, n+1, n+1+opts.Context)
					}
					sh.matches = append(sh.matches, m)
				}
				atomic.AddInt64(&found, int64(len(sh.matches)-before))
			}
		}(w)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	result := &GrepResult{Matches: []Match{}, Commit: commitID.String()}
	for i := range shards {
		result.Matches = append(result.Matches, shards[i].matches...)
		result.Scanned += shards[i].scanned
		result.Matched += shards[i].matched
		result.Skipped += shards[i].skipped
	}
	sort.Slice(result.Matches, func(i, j int) bool {
		if result.Matches[i].Path != result.Matches[j].Path {
			return result.Matches[i].Path < result.Matches[j].Path
		}
		return result.Matches[i].Line < result.Matches[j].Line
	})
	// Truncated means "there is more to find", not "some files were not
	// text". Deriving it from the scanned count made every repository with
	// a binary in it report a truncated search, which tells an agent to
	// narrow a search that was already exhaustive.
	if atomic.LoadInt64(&found) >= stopAfter {
		result.Truncated = true
	}
	if len(result.Matches) > opts.Limit {
		result.Matches = result.Matches[:opts.Limit]
		result.Truncated = true
	}

	if opts.IncludeFiles {
		blobs := make(map[string]object.ID, len(files))
		for _, f := range files {
			blobs[f.path] = f.blob
		}
		if err := attachFiles(ctx, s, result, blobs); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// attachFiles adds the matched files in full, in the order they first
// appear in the matches, and stops at the limits rather than truncating a
// file. Half a source file is worse than none: the agent cannot tell which
// half it has, and an edit anchored in the missing part fails for a reason
// that looks like the file changed underneath it.
func attachFiles(ctx context.Context, s store.Store, res *GrepResult, blobs map[string]object.ID) error {
	seen := map[string]bool{}
	for _, m := range res.Matches {
		if seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		if len(res.Files) >= maxGrepFiles {
			return nil
		}
		id, ok := blobs[m.Path]
		if !ok {
			continue
		}
		obj, err := s.Get(ctx, id)
		if err != nil {
			return err
		}
		if len(obj.Payload) > maxGrepFileBytes || isBinary(obj.Payload) {
			continue
		}
		res.Files = append(res.Files, GrepFile{Path: m.Path, Content: string(obj.Payload)})
	}
	return nil
}

var errStopWalk = errors.New("stop walk")

func matchesPrefix(p string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		prefix = cleanPath(prefix)
		if prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func sliceLines(lines []string, from, to int) []string {
	if from < 0 {
		from = 0
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from >= to {
		return nil
	}
	out := make([]string, to-from)
	copy(out, lines[from:to])
	return out
}

// isBinary uses git's own heuristic: a NUL byte near the start means the
// content is not text.
func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// walkTree visits every blob under a tree, depth first, with full paths.
func walkTree(ctx context.Context, s store.Store, treeID object.ID, prefix string,
	fn func(path string, blob object.ID) error) error {

	tree, err := readTree(ctx, s, treeID)
	if err != nil {
		return err
	}
	for _, e := range tree.Entries {
		full := e.Name
		if prefix != "" {
			full = prefix + "/" + e.Name
		}
		switch {
		case e.Mode.IsSubmodule():
			// The commit a gitlink names lives in another repository.
			continue
		case e.Mode.IsDir():
			if err := walkTree(ctx, s, e.ID, full, fn); err != nil {
				return err
			}
		default:
			if err := fn(full, e.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListFiles returns every file path at a revision, optionally filtered by
// a glob such as "src/**/*.go".
func ListFiles(ctx context.Context, s store.Store, rev, glob string) ([]string, error) {
	treeID, err := CommitTree(ctx, s, rev)
	if err != nil {
		return nil, err
	}
	var out []string
	err = walkTree(ctx, s, treeID, "", func(p string, _ object.ID) error {
		if glob == "" || matchGlob(glob, p) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// matchGlob supports the "**" segment that path.Match lacks, because a
// pattern like "src/**/*.go" is the common case for an agent narrowing a
// search and path.Match would never match across a slash.
func matchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		return err == nil && ok
	}
	parts := strings.SplitN(pattern, "**", 2)
	prefix, suffix := parts[0], parts[1]
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		return true
	}
	// The suffix may match at any depth below the prefix.
	for {
		if ok, err := path.Match(suffix, rest); err == nil && ok {
			return true
		}
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
}

// Fork points a new branch at an existing revision.
//
// No object data is copied. A fork is one reference write, which is why
// branching a repository for a new agent is free however large it is.
func Fork(ctx context.Context, s store.Store, from, newBranch string) (object.ID, error) {
	target, err := Resolve(ctx, s, from)
	if err != nil {
		return object.ID{}, err
	}
	name := newBranch
	if !strings.HasPrefix(name, "refs/") {
		name = "refs/heads/" + name
	}
	// A zero Old means the branch must not already exist, so a fork never
	// silently overwrites someone else's work.
	err = s.UpdateRefs(ctx, []store.RefUpdate{{Name: name, Old: object.ID{}, New: target}})
	if err != nil {
		return object.ID{}, err
	}
	return target, nil
}
