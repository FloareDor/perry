package protocol

import (
	"context"
	"errors"
	"fmt"

	"github.com/perry-dev/perry/internal/object"
	"github.com/perry-dev/perry/internal/store"
)

// Walk collects every object reachable from want and not reachable from
// have. This is the set a fetch has to send.
//
// The walk runs in two phases. First it marks everything the client already
// has, then it collects from the wants and skips anything marked. Doing it
// in one pass would be wrong: an object can be reachable from both sides,
// and whichever side the traversal reached first would decide whether it
// got sent.
func Walk(ctx context.Context, s store.ObjectStore, want, have []object.ID) ([]*object.Object, error) {
	seen := make(map[object.ID]bool)

	// Phase one: everything the client already has.
	for _, id := range have {
		if err := mark(ctx, s, id, seen); err != nil {
			// A client may claim to have an object this server has never
			// stored, for instance after the server was restored from a
			// backup. That is not fatal: it just means nothing can be
			// excluded on account of it.
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
	}

	// Phase two: collect what the client is missing.
	var out []*object.Object
	collected := make(map[object.ID]bool)
	for _, id := range want {
		if err := collect(ctx, s, id, seen, collected, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mark records an object and everything it reaches, without loading more
// than it has to.
func mark(ctx context.Context, s store.ObjectStore, id object.ID, seen map[object.ID]bool) error {
	stack := []object.ID{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] || cur.IsZero() {
			continue
		}
		seen[cur] = true

		obj, err := s.Get(ctx, cur)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		children, err := childrenOf(obj)
		if err != nil {
			return err
		}
		stack = append(stack, children...)
	}
	return nil
}

// collect gathers an object and its reachable set, skipping anything the
// client already has.
func collect(ctx context.Context, s store.ObjectStore, root object.ID,
	seen, collected map[object.ID]bool, out *[]*object.Object) error {

	stack := []object.ID{root}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.IsZero() || seen[cur] || collected[cur] {
			continue
		}

		obj, err := s.Get(ctx, cur)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// A gitlink points at a commit in another repository, which
				// this one is not expected to hold.
				continue
			}
			return fmt.Errorf("walk: %s: %w", cur, err)
		}
		collected[cur] = true
		*out = append(*out, obj)

		children, err := childrenOf(obj)
		if err != nil {
			return err
		}
		stack = append(stack, children...)
	}
	return nil
}

// childrenOf returns the objects an object points at: a commit names its
// tree and its parents, a tree names its entries, a tag names its target,
// and a blob names nothing.
func childrenOf(obj *object.Object) ([]object.ID, error) {
	switch obj.Type {
	case object.TypeCommit:
		c, err := object.DecodeCommit(obj.Payload)
		if err != nil {
			return nil, fmt.Errorf("walk: decode commit: %w", err)
		}
		out := make([]object.ID, 0, len(c.Parents)+1)
		out = append(out, c.Tree)
		return append(out, c.Parents...), nil

	case object.TypeTree:
		t, err := object.DecodeTree(obj.Payload)
		if err != nil {
			return nil, fmt.Errorf("walk: decode tree: %w", err)
		}
		out := make([]object.ID, 0, len(t.Entries))
		for _, e := range t.Entries {
			// A gitlink names a commit in a different repository. Following
			// it would walk into a repo this server does not have.
			if e.Mode.IsSubmodule() {
				continue
			}
			out = append(out, e.ID)
		}
		return out, nil

	case object.TypeTag:
		tg, err := object.DecodeTag(obj.Payload)
		if err != nil {
			return nil, fmt.Errorf("walk: decode tag: %w", err)
		}
		return []object.ID{tg.Object}, nil

	default:
		return nil, nil
	}
}

// Reachable reports whether target is reachable from any of the roots. The
// server uses it to refuse a fetch for an object no advertised ref leads to.
func Reachable(ctx context.Context, s store.ObjectStore, roots []object.ID, target object.ID) (bool, error) {
	seen := make(map[object.ID]bool)
	stack := append([]object.ID{}, roots...)

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == target {
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
		children, err := childrenOf(obj)
		if err != nil {
			return false, err
		}
		stack = append(stack, children...)
	}
	return false, nil
}
