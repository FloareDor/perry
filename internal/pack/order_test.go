package pack

import (
	"testing"

	"github.com/FloareDor/perry/internal/object"
)

// Git's pack_name_hash on a handful of paths, computed by the reference
// implementation. Matching git here is not required for correctness, but
// it means the two packers agree on what "near" means.
func TestNameHashMatchesGit(t *testing.T) {
	cases := map[string]uint32{
		"":            0,
		"a":           0x61000000,
		"README.md":   0x83977600,
		"src/main.go": 0x8de152b0,
	}
	for path, want := range cases {
		if got := NameHash(path); got != want {
			t.Errorf("NameHash(%q) = %#x, want %#x", path, got, want)
		}
	}
}

func TestOrderGroupsRevisionsOfOneFile(t *testing.T) {
	blobA1 := &object.Object{Type: object.TypeBlob, Payload: []byte("alpha one")}
	blobA2 := &object.Object{Type: object.TypeBlob, Payload: []byte("alpha two, longer")}
	blobB := &object.Object{Type: object.TypeBlob, Payload: []byte("bravo bravo bravo bravo")}

	tree1 := (&object.Tree{Entries: []object.TreeEntry{
		{Mode: object.ModeRegular, Name: "a.txt", ID: blobA1.ID()},
		{Mode: object.ModeRegular, Name: "b.txt", ID: blobB.ID()},
	}}).Object()
	tree2 := (&object.Tree{Entries: []object.TreeEntry{
		{Mode: object.ModeRegular, Name: "a.txt", ID: blobA2.ID()},
		{Mode: object.ModeRegular, Name: "b.txt", ID: blobB.ID()},
	}}).Object()

	objs := []*object.Object{blobB, tree2, blobA1, tree1, blobA2}
	hints := PathHints(objs)
	if len(hints) != 5 {
		t.Fatalf("expected a hint for all 5 objects, got %d", len(hints))
	}
	if hints[blobA1.ID()] != hints[blobA2.ID()] {
		t.Fatal("two revisions of a.txt should share a hint")
	}

	ordered := Order(objs, hints)
	pos := map[object.ID]int{}
	for i, o := range ordered {
		pos[o.ID()] = i
	}
	if d := pos[blobA1.ID()] - pos[blobA2.ID()]; d != 1 && d != -1 {
		t.Errorf("revisions of a.txt are %d apart, want adjacent", d)
	}
	if ordered[0].Type != object.TypeTree {
		t.Errorf("trees should sort before blobs, got %s first", ordered[0].Type)
	}
}
