package pack

import (
	"sort"

	"github.com/perry-dev/perry/internal/object"
)

// NameHash is git's pack_name_hash: a cheap hash of a path that keeps
// revisions of the same file, and files with the same name in different
// directories, close together once objects are sorted by it. Whitespace is
// skipped so renames that only reindent a path still collide. The last
// sixteen characters dominate, which is deliberate: the basename matters
// more than the directory.
func NameHash(path string) uint32 {
	var h uint32
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		h = (h >> 2) + uint32(c)<<24
	}
	return h
}

// PathHints walks the commits and trees in objs and assigns each tree and
// blob the hash of the first path it is seen at. Objects reachable from no
// tree in the set, which happens on an incremental fetch where the client
// already holds the enclosing trees, get no hint and sort after the rest.
func PathHints(objs []*object.Object) map[object.ID]uint32 {
	byID := make(map[object.ID]*object.Object, len(objs))
	for _, o := range objs {
		byID[o.ID()] = o
	}

	hints := make(map[object.ID]uint32, len(objs))
	var walk func(id object.ID, prefix string)
	walk = func(id object.ID, prefix string) {
		o := byID[id]
		if o == nil || o.Type != object.TypeTree {
			return
		}
		if _, seen := hints[id]; seen {
			return
		}
		hints[id] = NameHash(prefix)
		t, err := object.DecodeTree(o.Payload)
		if err != nil {
			return
		}
		for _, e := range t.Entries {
			path := prefix + e.Name
			if e.Mode.IsDir() {
				walk(e.ID, path+"/")
				continue
			}
			if _, seen := hints[e.ID]; !seen {
				hints[e.ID] = NameHash(path)
			}
		}
	}

	for _, o := range objs {
		if o.Type != object.TypeCommit {
			continue
		}
		c, err := object.DecodeCommit(o.Payload)
		if err != nil {
			continue
		}
		walk(c.Tree, "")
	}
	// Trees that no commit in the set names, such as the root of a tree the
	// client partly has, still order their own children.
	for _, o := range objs {
		if o.Type == object.TypeTree {
			walk(o.ID(), "")
		}
	}
	return hints
}

// Order sorts objects for packing: by type, then by path hash, then by
// descending size. With no hints this is the type-and-size order; with
// them, successive revisions of a file land next to each other and the
// delta window rarely needs to be wider than ten.
func Order(objs []*object.Object, hints map[object.ID]uint32) []*object.Object {
	type keyed struct {
		obj  *object.Object
		hash uint32
		has  bool
		size int
	}
	keys := make([]keyed, len(objs))
	for i, o := range objs {
		h, ok := hints[o.ID()]
		keys[i] = keyed{obj: o, hash: h, has: ok, size: len(o.Payload)}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.obj.Type != b.obj.Type {
			return a.obj.Type < b.obj.Type
		}
		if a.has != b.has {
			return a.has
		}
		if a.hash != b.hash {
			return a.hash < b.hash
		}
		return a.size > b.size
	})
	out := make([]*object.Object, len(keys))
	for i, k := range keys {
		out[i] = k.obj
	}
	return out
}
