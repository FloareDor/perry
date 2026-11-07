package object

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signature is an author or committer line: a name, an email, and a timestamp
// with the offset that was in force where it was written.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// String renders the signature in git's wire form:
// "Name <email> <unix seconds> <+HHMM>".
func (s Signature) String() string {
	return fmt.Sprintf("%s <%s> %d %s", s.Name, s.Email, s.When.Unix(), s.When.Format("-0700"))
}

// parseSignature reads a signature line. The timestamp is optional because
// some old history in the wild omits it.
func parseSignature(line string) (Signature, error) {
	var sig Signature

	lt := strings.LastIndex(line, " <")
	gt := strings.LastIndex(line, "> ")
	if lt < 0 || gt < 0 || gt < lt {
		// No timestamp: the line may end at the closing angle bracket.
		if lt >= 0 && strings.HasSuffix(line, ">") {
			sig.Name = line[:lt]
			sig.Email = line[lt+2 : len(line)-1]
			return sig, nil
		}
		return sig, fmt.Errorf("%w: signature %q has no email", ErrMalformed, line)
	}
	sig.Name = line[:lt]
	sig.Email = line[lt+2 : gt]

	rest := strings.Fields(line[gt+2:])
	if len(rest) == 0 {
		return sig, nil
	}
	secs, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return sig, fmt.Errorf("%w: bad signature timestamp %q", ErrMalformed, rest[0])
	}

	loc := time.UTC
	if len(rest) > 1 {
		if off, err := parseTZOffset(rest[1]); err == nil {
			loc = time.FixedZone(rest[1], off)
		}
	}
	sig.When = time.Unix(secs, 0).In(loc)
	return sig, nil
}

// parseTZOffset converts a "+HHMM" or "-HHMM" zone to seconds east of UTC.
func parseTZOffset(s string) (int, error) {
	if len(s) != 5 {
		return 0, fmt.Errorf("bad zone %q", s)
	}
	sign := 1
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, fmt.Errorf("bad zone sign in %q", s)
	}
	h, err := strconv.Atoi(s[1:3])
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(s[3:5])
	if err != nil {
		return 0, err
	}
	return sign * (h*3600 + m*60), nil
}

// Commit is a snapshot: a tree, zero or more parents, who wrote it, and why.
//
// Extra holds header lines perry does not model, such as "gpgsig" or
// "encoding", in their original order. Keeping them verbatim matters because
// re-encoding a commit that dropped a header would change its hash.
type Commit struct {
	Tree      ID
	Parents   []ID
	Author    Signature
	Committer Signature
	Extra     []ExtraHeader
	Message   string
}

// ExtraHeader is a commit or tag header perry passes through unchanged.
// Value keeps its embedded newlines; the leading space git uses to continue a
// folded header is stripped on parse and restored on encode.
type ExtraHeader struct {
	Key   string
	Value string
}

// DecodeCommit parses a commit payload: headers, a blank line, then the
// message.
func DecodeCommit(payload []byte) (*Commit, error) {
	c := &Commit{}
	headers, message, err := splitHeaders(payload)
	if err != nil {
		return nil, err
	}
	c.Message = message

	for _, h := range headers {
		switch h.Key {
		case "tree":
			if c.Tree, err = ParseID(h.Value); err != nil {
				return nil, fmt.Errorf("%w: commit tree: %v", ErrMalformed, err)
			}
		case "parent":
			p, err := ParseID(h.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: commit parent: %v", ErrMalformed, err)
			}
			c.Parents = append(c.Parents, p)
		case "author":
			if c.Author, err = parseSignature(h.Value); err != nil {
				return nil, err
			}
		case "committer":
			if c.Committer, err = parseSignature(h.Value); err != nil {
				return nil, err
			}
		default:
			c.Extra = append(c.Extra, h)
		}
	}
	if c.Tree.IsZero() {
		return nil, fmt.Errorf("%w: commit has no tree header", ErrMalformed)
	}
	return c, nil
}

// Encode serializes the commit in git's canonical header order: tree, then
// parents in order, then author, then committer, then any pass-through
// headers, then a blank line and the message.
func (c *Commit) Encode() []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "tree %s\n", c.Tree)
	for _, p := range c.Parents {
		fmt.Fprintf(&buf, "parent %s\n", p)
	}
	fmt.Fprintf(&buf, "author %s\n", c.Author)
	fmt.Fprintf(&buf, "committer %s\n", c.Committer)
	for _, h := range c.Extra {
		writeFoldedHeader(&buf, h)
	}
	buf.WriteByte('\n')
	buf.WriteString(c.Message)
	return buf.Bytes()
}

// Object returns the commit as a generic object ready to store.
func (c *Commit) Object() *Object { return &Object{Type: TypeCommit, Payload: c.Encode()} }

// Tag is an annotated tag object.
type Tag struct {
	Object  ID
	ObjType Type
	Name    string
	Tagger  Signature
	Extra   []ExtraHeader
	Message string
}

// DecodeTag parses a tag payload.
func DecodeTag(payload []byte) (*Tag, error) {
	t := &Tag{}
	headers, message, err := splitHeaders(payload)
	if err != nil {
		return nil, err
	}
	t.Message = message

	for _, h := range headers {
		switch h.Key {
		case "object":
			if t.Object, err = ParseID(h.Value); err != nil {
				return nil, fmt.Errorf("%w: tag object: %v", ErrMalformed, err)
			}
		case "type":
			if t.ObjType, err = ParseType(h.Value); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
			}
		case "tag":
			t.Name = h.Value
		case "tagger":
			if t.Tagger, err = parseSignature(h.Value); err != nil {
				return nil, err
			}
		default:
			t.Extra = append(t.Extra, h)
		}
	}
	if t.Object.IsZero() {
		return nil, fmt.Errorf("%w: tag has no object header", ErrMalformed)
	}
	return t, nil
}

// Encode serializes the tag.
func (t *Tag) Encode() []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "object %s\n", t.Object)
	fmt.Fprintf(&buf, "type %s\n", t.ObjType)
	fmt.Fprintf(&buf, "tag %s\n", t.Name)
	if t.Tagger.Name != "" {
		fmt.Fprintf(&buf, "tagger %s\n", t.Tagger)
	}
	for _, h := range t.Extra {
		writeFoldedHeader(&buf, h)
	}
	buf.WriteByte('\n')
	buf.WriteString(t.Message)
	return buf.Bytes()
}

// Object returns the tag as a generic object ready to store.
func (t *Tag) Object_() *Object { return &Object{Type: TypeTag, Payload: t.Encode()} }

// splitHeaders divides a commit or tag payload at the first blank line.
//
// A header value continues onto following lines that begin with a space; git
// uses this to embed a PGP signature in a single logical header. The leading
// space is removed here and put back by writeFoldedHeader.
func splitHeaders(payload []byte) ([]ExtraHeader, string, error) {
	var headers []ExtraHeader
	rest := payload

	for {
		if len(rest) == 0 {
			return headers, "", nil
		}
		if rest[0] == '\n' {
			return headers, string(rest[1:]), nil
		}

		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			// Final line with no trailing newline: treat it as a header.
			line := string(rest)
			k, v := splitHeaderLine(line)
			headers = append(headers, ExtraHeader{Key: k, Value: v})
			return headers, "", nil
		}

		line := string(rest[:nl])
		rest = rest[nl+1:]

		key, value := splitHeaderLine(line)
		for len(rest) > 0 && rest[0] == ' ' {
			cont := rest
			nl := bytes.IndexByte(cont, '\n')
			if nl < 0 {
				value += "\n" + string(cont[1:])
				rest = nil
				break
			}
			value += "\n" + string(cont[1:nl])
			rest = cont[nl+1:]
		}
		headers = append(headers, ExtraHeader{Key: key, Value: value})
	}
}

func splitHeaderLine(line string) (key, value string) {
	if sp := strings.IndexByte(line, ' '); sp >= 0 {
		return line[:sp], line[sp+1:]
	}
	return line, ""
}

func writeFoldedHeader(buf *bytes.Buffer, h ExtraHeader) {
	buf.WriteString(h.Key)
	for i, line := range strings.Split(h.Value, "\n") {
		if i == 0 {
			buf.WriteByte(' ')
		} else {
			buf.WriteString("\n ")
		}
		buf.WriteString(line)
	}
	buf.WriteByte('\n')
}
