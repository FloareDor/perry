package object

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Well-known object names. The empty blob and empty tree have fixed SHA-1s
// that every git installation agrees on, so they pin our hashing to git's.
const (
	emptyBlobID = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	emptyTreeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

func TestHashMatchesWellKnownIDs(t *testing.T) {
	if got := Hash(TypeBlob, nil).String(); got != emptyBlobID {
		t.Errorf("empty blob = %s, want %s", got, emptyBlobID)
	}
	if got := Hash(TypeTree, nil).String(); got != emptyTreeID {
		t.Errorf("empty tree = %s, want %s", got, emptyTreeID)
	}
	// "hello\n" is the canonical git hash-object smoke test.
	const helloID = "ce013625030ba8dba906f756967f9e9ca394464a"
	if got := Hash(TypeBlob, []byte("hello\n")).String(); got != helloID {
		t.Errorf("blob 'hello\\n' = %s, want %s", got, helloID)
	}
}

func TestParseIDRoundTrip(t *testing.T) {
	id, err := ParseID(emptyBlobID)
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != emptyBlobID {
		t.Errorf("round trip = %s, want %s", id, emptyBlobID)
	}
	if id.Short(7) != emptyBlobID[:7] {
		t.Errorf("Short(7) = %s", id.Short(7))
	}
	for _, bad := range []string{"", "abc", strings.Repeat("z", 40), emptyBlobID + "0"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) should have failed", bad)
		}
	}
}

func TestLooseRoundTrip(t *testing.T) {
	for _, o := range []*Object{
		{Type: TypeBlob, Payload: nil},
		{Type: TypeBlob, Payload: []byte("hello\n")},
		{Type: TypeBlob, Payload: bytes.Repeat([]byte{0}, 4096)},
		{Type: TypeCommit, Payload: []byte("tree " + emptyTreeID + "\n\nmsg\n")},
	} {
		enc, err := MarshalLoose(o)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalLoose(enc)
		if err != nil {
			t.Fatalf("decode %s: %v", o.Type, err)
		}
		if got.Type != o.Type || !bytes.Equal(got.Payload, o.Payload) {
			t.Errorf("round trip changed the object: %s/%d -> %s/%d",
				o.Type, len(o.Payload), got.Type, len(got.Payload))
		}
	}
}

func TestDecodeLooseRejectsCorruption(t *testing.T) {
	good, err := MarshalLoose(&Object{Type: TypeBlob, Payload: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	// Truncating the deflate stream must be an error, not a short read.
	if _, err := UnmarshalLoose(good[:len(good)-4]); err == nil {
		t.Error("truncated object decoded without error")
	}
	if _, err := UnmarshalLoose([]byte("not zlib at all")); err == nil {
		t.Error("garbage decoded without error")
	}
	// A header whose declared size disagrees with the payload is corrupt.
	var buf bytes.Buffer
	if err := EncodeLoose(&buf, &Object{Type: TypeBlob, Payload: []byte("hi")}); err != nil {
		t.Fatal(err)
	}
	mismatched := mustDeflate(t, "blob 99\x00hi")
	if _, err := UnmarshalLoose(mismatched); err == nil {
		t.Error("size mismatch decoded without error")
	}
}

func TestTreeSortsLikeGit(t *testing.T) {
	// Git sorts a directory as though its name ended in "/". The keys here
	// are "a.c", "a/" and "a0", and because '.' (0x2E) < '/' (0x2F) < '0'
	// (0x30) the directory lands between the two files. Sorting the names
	// plainly would put "a" first and change the tree's hash.
	tr := &Tree{Entries: []TreeEntry{
		{Mode: ModeDir, Name: "a", ID: MustParseID(emptyTreeID)},
		{Mode: ModeRegular, Name: "a.c", ID: MustParseID(emptyBlobID)},
		{Mode: ModeRegular, Name: "a0", ID: MustParseID(emptyBlobID)},
	}}
	got, err := DecodeTree(tr.Encode())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.c", "a", "a0"}
	for i, e := range got.Entries {
		if e.Name != want[i] {
			t.Fatalf("entry %d = %q, want %q (full order %v)", i, e.Name, want[i], names(got))
		}
	}
}

func TestTreeRoundTrip(t *testing.T) {
	tr := &Tree{Entries: []TreeEntry{
		{Mode: ModeRegular, Name: "README.md", ID: MustParseID(emptyBlobID)},
		{Mode: ModeExec, Name: "build.sh", ID: MustParseID(emptyBlobID)},
		{Mode: ModeDir, Name: "src", ID: MustParseID(emptyTreeID)},
		{Mode: ModeSymlink, Name: "link", ID: MustParseID(emptyBlobID)},
	}}
	enc := tr.Encode()
	got, err := DecodeTree(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(got.Entries))
	}
	if !bytes.Equal(got.Encode(), enc) {
		t.Error("re-encoding a decoded tree changed the bytes")
	}
	if e, ok := got.Lookup("build.sh"); !ok || e.Mode != ModeExec {
		t.Errorf("build.sh lookup = %+v, %v", e, ok)
	}
}

func TestDecodeTreeRejectsTruncation(t *testing.T) {
	tr := &Tree{Entries: []TreeEntry{{Mode: ModeRegular, Name: "f", ID: MustParseID(emptyBlobID)}}}
	enc := tr.Encode()
	if _, err := DecodeTree(enc[:len(enc)-5]); err == nil {
		t.Error("truncated tree decoded without error")
	}
}

func TestCommitEncodeIsByteStable(t *testing.T) {
	// Re-encoding a parsed commit must reproduce the original bytes exactly.
	// If it does not, the commit's hash changes and history breaks.
	raw := "tree " + emptyTreeID + "\n" +
		"parent " + emptyBlobID + "\n" +
		"author Ada <ada@example.com> 1700000000 +0000\n" +
		"committer Ada <ada@example.com> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" \n" +
		" iQEzBAAB\n" +
		" -----END PGP SIGNATURE-----\n" +
		"\n" +
		"Subject line\n\nBody text.\n"

	c, err := DecodeCommit([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tree.String() != emptyTreeID {
		t.Errorf("tree = %s", c.Tree)
	}
	if len(c.Parents) != 1 {
		t.Errorf("got %d parents, want 1", len(c.Parents))
	}
	if c.Author.Name != "Ada" || c.Author.Email != "ada@example.com" {
		t.Errorf("author = %+v", c.Author)
	}
	if c.Author.When.Unix() != 1700000000 {
		t.Errorf("author time = %d", c.Author.When.Unix())
	}
	if len(c.Extra) != 1 || c.Extra[0].Key != "gpgsig" {
		t.Fatalf("extra headers = %+v", c.Extra)
	}
	if !strings.Contains(c.Message, "Subject line") {
		t.Errorf("message = %q", c.Message)
	}
	if got := string(c.Encode()); got != raw {
		t.Errorf("re-encode differs.\n got: %q\nwant: %q", got, raw)
	}
}

func TestCommitTimezoneIsPreserved(t *testing.T) {
	raw := "tree " + emptyTreeID + "\n" +
		"author Ada <a@b.c> 1700000000 -0500\n" +
		"committer Ada <a@b.c> 1700000000 -0500\n\nm\n"
	c, err := DecodeCommit([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, off := c.Author.When.Zone()
	if off != -5*3600 {
		t.Errorf("zone offset = %d seconds, want -18000", off)
	}
	if string(c.Encode()) != raw {
		t.Errorf("re-encode lost the zone: %q", c.Encode())
	}
}

func TestTagRoundTrip(t *testing.T) {
	raw := "object " + emptyTreeID + "\n" +
		"type commit\n" +
		"tag v1.0.0\n" +
		"tagger Ada <a@b.c> 1700000000 +0000\n\nRelease.\n"
	tg, err := DecodeTag([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tg.Name != "v1.0.0" || tg.ObjType != TypeCommit {
		t.Errorf("tag = %+v", tg)
	}
	if string(tg.Encode()) != raw {
		t.Errorf("re-encode differs: %q", tg.Encode())
	}
}

// TestAgainstRealGit cross-checks our hashing with the git binary. It is the
// test that matters most: agreeing with ourselves proves nothing.
func TestAgainstRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}

	cases := []struct {
		name    string
		typ     Type
		payload []byte
	}{
		{"empty blob", TypeBlob, nil},
		{"text blob", TypeBlob, []byte("hello world\n")},
		{"binary blob", TypeBlob, append([]byte{0, 1, 2, 0xff}, bytes.Repeat([]byte("x"), 1000)...)},
		{"large blob", TypeBlob, bytes.Repeat([]byte("abcdefgh"), 100000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(git, "hash-object", "-t", tc.typ.String(), "--stdin")
			cmd.Stdin = bytes.NewReader(tc.payload)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git hash-object: %v", err)
			}
			want := strings.TrimSpace(string(out))
			if got := Hash(tc.typ, tc.payload).String(); got != want {
				t.Errorf("perry = %s, git = %s", got, want)
			}
		})
	}

	// A tree built by perry must hash the same as the one git builds from the
	// same entries, which exercises both the sort order and the encoding.
	t.Run("tree", func(t *testing.T) {
		tr := &Tree{Entries: []TreeEntry{
			{Mode: ModeRegular, Name: "a.c", ID: MustParseID(emptyBlobID)},
			{Mode: ModeDir, Name: "a", ID: MustParseID(emptyTreeID)},
			{Mode: ModeExec, Name: "run.sh", ID: MustParseID(emptyBlobID)},
		}}
		cmd := exec.Command(git, "hash-object", "-t", "tree", "--stdin")
		cmd.Stdin = bytes.NewReader(tr.Encode())
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git hash-object: %v", err)
		}
		want := strings.TrimSpace(string(out))
		if got := tr.Object().ID().String(); got != want {
			t.Errorf("perry tree = %s, git = %s", got, want)
		}
	})
}

func TestSignatureString(t *testing.T) {
	s := Signature{Name: "Ada", Email: "a@b.c", When: time.Unix(1700000000, 0).UTC()}
	if got := s.String(); got != "Ada <a@b.c> 1700000000 +0000" {
		t.Errorf("= %q", got)
	}
}

func names(t *Tree) []string {
	out := make([]string, len(t.Entries))
	for i, e := range t.Entries {
		out[i] = e.Name
	}
	return out
}

func mustDeflate(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := newZlibWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
