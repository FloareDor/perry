package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/FloareDor/perry/internal/object"
	"github.com/FloareDor/perry/internal/store"
)

// Change is one file edit in a commit.
//
// A nil Content deletes the path. Using nil rather than an empty slice keeps
// "write an empty file" and "remove this file" distinguishable, which a
// struct with a bool would muddle.
type Change struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Delete  bool   `json:"delete,omitempty"`
	Mode    string `json:"mode,omitempty"`
	// Edits replaces runs of text in the file that is already there,
	// instead of sending the whole thing. It is mutually exclusive with
	// Content, and the file must exist on the branch being committed to.
	Edits []Edit `json:"edits,omitempty"`
}

// CommitRequest describes a commit to create without a working tree.
type CommitRequest struct {
	Branch  string   `json:"branch"`
	Message string   `json:"message"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Changes []Change `json:"changes"`
	// Expect pins the commit the branch must currently point at. Leaving it
	// empty reads the current tip instead, which races with a concurrent
	// writer; an agent that read the branch earlier should send what it saw.
	Expect string    `json:"expect,omitempty"`
	When   time.Time `json:"-"`
}

// CommitResult reports a created commit.
type CommitResult struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
	Branch string `json:"branch"`
	Parent string `json:"parent,omitempty"`
}

// Commit writes a commit directly into the object store.
//
// This is what lets an agent change a repository without ever checking one
// out: it reads the files it needs through the read API, sends back the
// edited contents, and perry builds the blobs, trees and commit itself. The
// branch moves under a compare-and-swap, so two agents editing the same
// branch cannot silently overwrite each other.
func Commit(ctx context.Context, s store.Store, req CommitRequest) (*CommitResult, error) {
	if req.Branch == "" {
		return nil, errors.New("api: commit needs a branch")
	}
	if len(req.Changes) == 0 {
		return nil, errors.New("api: commit needs at least one change")
	}
	if req.Message == "" {
		req.Message = "commit via perry"
	}
	if req.Author == "" {
		req.Author = "perry"
	}
	if req.Email == "" {
		req.Email = "perry@localhost"
	}
	if req.When.IsZero() {
		req.When = time.Now()
	}

	refName := req.Branch
	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	// Find the parent commit and the tree to build on. A branch that does
	// not exist yet produces a root commit on an empty tree.
	var parent object.ID
	var baseTree object.ID
	ref, err := s.GetRef(ctx, refName)
	switch {
	case err == nil:
		parent = ref.Target
		baseTree, err = CommitTree(ctx, s, refName)
		if err != nil {
			return nil, err
		}
	case errors.Is(err, store.ErrNotFound):
		// Leave parent and baseTree zero.
	default:
		return nil, err
	}

	if req.Expect != "" {
		expect, err := object.ParseID(req.Expect)
		if err != nil {
			return nil, fmt.Errorf("api: bad expect: %w", err)
		}
		if expect != parent {
			return nil, &store.ErrRefConflict{Ref: refName, Expected: expect, Actual: parent}
		}
	}

	// Write the blobs first, so the tree being built always references
	// objects that already exist.
	edits := make(map[string]*object.TreeEntry, len(req.Changes))
	// written remembers what each path's contents became earlier in this
	// same request, so two changes to one file compose instead of the
	// second one silently undoing the first.
	written := map[string][]byte{}
	var toStore []*object.Object
	for _, c := range req.Changes {
		clean := cleanPath(c.Path)
		if clean == "" {
			return nil, fmt.Errorf("api: invalid path %q", c.Path)
		}
		if c.Delete {
			if len(c.Edits) > 0 {
				return nil, fmt.Errorf("api: %s cannot be both edited and deleted", clean)
			}
			edits[clean] = nil
			delete(written, clean)
			continue
		}

		mode := object.ModeRegular
		switch c.Mode {
		case "100755", "755":
			mode = object.ModeExec
		case "120000":
			mode = object.ModeSymlink
		}

		content := c.Content
		if len(c.Edits) > 0 {
			if c.Content != nil {
				return nil, fmt.Errorf("api: %s has both content and edits; send one or the other", clean)
			}
			base, ok := written[clean]
			if !ok {
				var baseMode object.Mode
				base, baseMode, err = blobAt(ctx, s, baseTree, clean)
				if err != nil {
					return nil, err
				}
				// An edit that does not ask for a mode keeps the one
				// the file already has.
				if c.Mode == "" {
					mode = baseMode
				}
			}
			content, err = applyEdits(clean, base, c.Edits)
			if err != nil {
				return nil, err
			}
		}
		written[clean] = content

		blob := &object.Object{Type: object.TypeBlob, Payload: content}
		toStore = append(toStore, blob)
		edits[clean] = &object.TreeEntry{Mode: mode, ID: blob.ID()}
	}
	if err := s.PutBatch(ctx, toStore); err != nil {
		return nil, err
	}

	newTree, err := applyToTree(ctx, s, baseTree, edits, "")
	if err != nil {
		return nil, err
	}

	// A commit that changes nothing would still get a new hash through its
	// timestamp, which quietly fills history with empty commits.
	if newTree == baseTree && !parent.IsZero() {
		return nil, errors.New("api: the changes produce no difference from the current tree")
	}

	sig := object.Signature{Name: req.Author, Email: req.Email, When: req.When}
	commit := &object.Commit{
		Tree:      newTree,
		Author:    sig,
		Committer: sig,
		Message:   strings.TrimRight(req.Message, "\n") + "\n",
	}
	if !parent.IsZero() {
		commit.Parents = []object.ID{parent}
	}

	commitObj := commit.Object()
	if _, err := s.Put(ctx, commitObj); err != nil {
		return nil, err
	}

	update := store.RefUpdate{Name: refName, Old: parent, New: commitObj.ID()}
	if err := s.UpdateRefs(ctx, []store.RefUpdate{update}); err != nil {
		return nil, err
	}

	res := &CommitResult{
		Commit: commitObj.ID().String(),
		Tree:   newTree.String(),
		Branch: refName,
	}
	if !parent.IsZero() {
		res.Parent = parent.String()
	}
	return res, nil
}

// applyToTree rebuilds a tree with a set of path edits applied.
//
// Git trees are immutable and name their children by hash, so changing one
// file means rewriting every tree from that file up to the root. The edits
// are grouped by their first path segment and the function recurses once per
// affected directory, which keeps the work proportional to the paths touched
// rather than to the size of the repository.
func applyToTree(ctx context.Context, s store.Store, treeID object.ID,
	edits map[string]*object.TreeEntry, prefix string) (object.ID, error) {

	existing := map[string]object.TreeEntry{}
	var order []string
	if !treeID.IsZero() {
		tree, err := readTree(ctx, s, treeID)
		if err != nil {
			return object.ID{}, err
		}
		for _, e := range tree.Entries {
			existing[e.Name] = e
			order = append(order, e.Name)
		}
	}

	// Split the edits for this level into direct children and subtrees.
	direct := map[string]*object.TreeEntry{}
	nested := map[string]map[string]*object.TreeEntry{}
	for path, entry := range edits {
		rel := path
		if prefix != "" {
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			rel = path[len(prefix)+1:]
		}
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			dir := rel[:i]
			if nested[dir] == nil {
				nested[dir] = map[string]*object.TreeEntry{}
			}
			nested[dir][path] = entry
			continue
		}
		direct[rel] = entry
	}

	for name, entry := range direct {
		if entry == nil {
			delete(existing, name)
			continue
		}
		e := *entry
		e.Name = name
		if _, had := existing[name]; !had {
			order = append(order, name)
		}
		existing[name] = e
	}

	for dir, sub := range nested {
		childPrefix := dir
		if prefix != "" {
			childPrefix = prefix + "/" + dir
		}
		var childID object.ID
		if e, ok := existing[dir]; ok && e.Mode.IsDir() {
			childID = e.ID
		}
		newChild, err := applyToTree(ctx, s, childID, sub, childPrefix)
		if err != nil {
			return object.ID{}, err
		}
		// A directory whose last file was deleted disappears, because git
		// has no concept of an empty directory.
		if newChild == object.Hash(object.TypeTree, nil) {
			delete(existing, dir)
			continue
		}
		if _, had := existing[dir]; !had {
			order = append(order, dir)
		}
		existing[dir] = object.TreeEntry{Mode: object.ModeDir, Name: dir, ID: newChild}
	}

	entries := make([]object.TreeEntry, 0, len(existing))
	seen := map[string]bool{}
	for _, name := range order {
		if e, ok := existing[name]; ok && !seen[name] {
			entries = append(entries, e)
			seen[name] = true
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	tree := &object.Tree{Entries: entries}
	obj := tree.Object()
	if _, err := s.Put(ctx, obj); err != nil {
		return object.ID{}, err
	}
	return obj.ID(), nil
}

// UpdateRefRequest is a compare-and-swap on one reference.
//
// Expect is the value the reference must currently hold. An empty Expect
// means it must not exist, and an empty Target deletes it. Making the
// caller state what it expects is what keeps two agents from overwriting
// each other's branch tips.
type UpdateRefRequest struct {
	Name   string `json:"name"`
	Expect string `json:"expect"`
	Target string `json:"target"`
	// Force allows a move that is not a fast-forward, which discards
	// commits the reference could reach and nothing else can.
	Force bool `json:"force,omitempty"`
}

// UpdateRef applies a single compare-and-swap reference update.
//
// Merging a finished agent branch is a pointer move, not a commit, so it
// needs its own endpoint rather than being squeezed through Commit.
func UpdateRef(ctx context.Context, s store.Store, req UpdateRefRequest) (*CommitResult, error) {
	if req.Name == "" {
		return nil, errors.New("api: ref update needs a name")
	}
	name := req.Name
	if !strings.HasPrefix(name, "refs/") {
		name = "refs/heads/" + name
	}

	var expect, target object.ID
	var err error
	if req.Expect != "" {
		if expect, err = object.ParseID(req.Expect); err != nil {
			return nil, fmt.Errorf("api: bad expect: %w", err)
		}
	}
	if req.Target != "" {
		if target, err = object.ParseID(req.Target); err != nil {
			return nil, fmt.Errorf("api: bad target: %w", err)
		}
		// Refusing to point a reference at an object the store does not
		// hold keeps a typo from producing a repository git cannot read.
		ok, err := s.Has(ctx, target)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: target %s", store.ErrNotFound, target)
		}
	}

	// Refuse a move that would drop history. A compare-and-swap alone does
	// not catch this: a caller that reads the tip and immediately swaps it
	// for an unrelated commit wins the race and loses the work. A merge
	// queue advancing a branch one agent at a time would silently keep only
	// the last agent's commit.
	if !req.Force && !expect.IsZero() && !target.IsZero() {
		ff, err := IsAncestor(ctx, s, expect, target)
		if err != nil {
			return nil, err
		}
		if !ff {
			return nil, fmt.Errorf("api: moving %s to %s is not a fast-forward from %s",
				name, target.Short(8), expect.Short(8))
		}
	}

	if err := s.UpdateRefs(ctx, []store.RefUpdate{{Name: name, Old: expect, New: target}}); err != nil {
		return nil, err
	}
	return &CommitResult{Branch: name, Commit: req.Target, Parent: req.Expect}, nil
}

// IsAncestor reports whether candidate is reachable from tip, which is the
// test for whether moving tip to candidate would be a fast-forward.
func IsAncestor(ctx context.Context, s store.Store, candidate, tip object.ID) (bool, error) {
	if candidate == tip {
		return true, nil
	}
	seen := map[object.ID]bool{}
	stack := []object.ID{tip}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == candidate {
			return true, nil
		}
		if cur.IsZero() || seen[cur] {
			continue
		}
		seen[cur] = true

		obj, err := s.Get(ctx, cur)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return false, err
		}
		if obj.Type != object.TypeCommit {
			continue
		}
		c, err := object.DecodeCommit(obj.Payload)
		if err != nil {
			return false, err
		}
		stack = append(stack, c.Parents...)
	}
	return false, nil
}
