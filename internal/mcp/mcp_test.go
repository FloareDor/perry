package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/mcp"
	"github.com/perry-dev/perry/internal/store"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

// fixture starts a perry HTTP API over an in-memory store seeded with a few
// files, and returns an MCP client pointed at it.
//
// The seeding goes through api.Commit rather than a git push, so the test
// needs no git binary and no network beyond loopback. The point under test is
// the adapter, not the wire protocol.
func fixture(t *testing.T) (*mcp.Client, store.Store) {
	t.Helper()

	// A file, not ":memory:". Grep fans out across goroutines and therefore
	// across pooled connections, and each connection to ":memory:" gets its
	// own empty database, so an in-memory store fails the moment a second
	// reader appears.
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	if _, err := api.Commit(ctx, s, api.CommitRequest{
		Branch:  "main",
		Message: "seed",
		Author:  "Fixture", Email: "fixture@example.com",
		Changes: []api.Change{
			{Path: "README.md", Content: []byte("# Demo\n\nA fixture repository.\n")},
			{Path: "src/main.go", Content: []byte("package main\n\nfunc main() {\n\tconnectTimeout := 30\n\t_ = connectTimeout\n}\n")},
			{Path: "src/pkg/util.go", Content: []byte("package pkg\n\nfunc Helper() int {\n\treturn 42\n}\n")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", &api.Handler{Open: func(string) (store.Store, error) { return s, nil }})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	c := mcp.NewClient(ts.URL, "demo", "")
	c.Author, c.Email = "Agent", "agent@perry.local"
	return c, s
}

// session drives a server over pipes the way an MCP client does, one
// newline-delimited frame at a time.
type session struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Reader
	id  int
}

func newSession(t *testing.T, c *mcp.Client) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	srv := mcp.NewServer(mcp.Tools(c))
	srv.Log = func(format string, a ...any) { t.Logf("server: "+format, a...) }

	go func() {
		_ = srv.Serve(context.Background(), inR, outW)
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close() })

	return &session{t: t, in: inW, out: bufio.NewReaderSize(outR, 1<<20)}
}

// call sends a request and returns the decoded reply.
func (s *session) call(method string, params any) map[string]any {
	s.t.Helper()
	s.id++
	s.send(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	return s.read()
}

// notify sends a message with no id, which must draw no reply.
func (s *session) notify(method string) {
	s.t.Helper()
	s.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

func (s *session) send(msg any) {
	s.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(data, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) sendRaw(line string) {
	s.t.Helper()
	if _, err := s.in.Write([]byte(line + "\n")); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) read() map[string]any {
	s.t.Helper()
	line, err := s.out.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("reading reply: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		s.t.Fatalf("reply is not JSON: %v: %s", err, line)
	}
	return m
}

// text pulls the model-visible text out of a tools/call reply, and reports
// whether the call was flagged as an error.
func (s *session) text(reply map[string]any) (string, bool) {
	s.t.Helper()
	if e, ok := reply["error"]; ok {
		s.t.Fatalf("expected a tool result, got a protocol error: %v", e)
	}
	res, ok := reply["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("reply has no result object: %v", reply)
	}
	isErr, _ := res["isError"].(bool)
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		s.t.Fatalf("result has no content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	txt, _ := first["text"].(string)
	return txt, isErr
}

func (s *session) tool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	return s.text(s.call("tools/call", map[string]any{"name": name, "arguments": args}))
}

// TestAgentSessionLocateReadEditCommit is the claim the adapter exists to
// support: a coding agent finds code, reads it, edits it and lands a commit
// without a clone, a checkout or a push, using nothing but MCP tool calls.
//
// Each step is asserted on its own, because a session that silently skipped
// the commit and still "succeeded" is exactly the failure worth catching.
func TestAgentSessionLocateReadEditCommit(t *testing.T) {
	c, s := fixture(t)
	sess := newSession(t, c)

	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	sess.notify("notifications/initialized")

	// 1. Locate.
	out, isErr := sess.tool("perry_grep", map[string]any{"pattern": "connectTimeout"})
	if isErr {
		t.Fatalf("grep failed: %s", out)
	}
	if !strings.Contains(out, "src/main.go:4:") {
		t.Fatalf("grep did not point at the right line:\n%s", out)
	}

	// 2. Read.
	content, isErr := sess.tool("perry_read_file", map[string]any{"path": "src/main.go"})
	if isErr {
		t.Fatalf("read failed: %s", content)
	}
	if !strings.Contains(content, "connectTimeout := 30") {
		t.Fatalf("read returned the wrong file:\n%s", content)
	}

	// 3. Branch.
	out, isErr = sess.tool("perry_create_branch", map[string]any{"branch": "agent/timeout", "from": "main"})
	if isErr {
		t.Fatalf("branch failed: %s", out)
	}

	// 4. Edit and commit the whole file back.
	edited := strings.Replace(content, "connectTimeout := 30", "connectTimeout := 60", 1)
	if edited == content {
		t.Fatal("the edit changed nothing, so the commit would prove nothing")
	}
	out, isErr = sess.tool("perry_commit", map[string]any{
		"branch":  "agent/timeout",
		"message": "raise the connect timeout",
		"changes": []map[string]any{{"path": "src/main.go", "content": edited}},
	})
	if isErr {
		t.Fatalf("commit failed: %s", out)
	}
	if !strings.Contains(out, "committed ") {
		t.Fatalf("commit result is not a commit: %s", out)
	}

	// 5. The edit is really in the repository, read back through the store
	// rather than through the same path that wrote it.
	got, err := api.ReadFile(context.Background(), s, "agent/timeout", "src/main.go")
	if err != nil {
		t.Fatalf("committed file missing: %v", err)
	}
	if !strings.Contains(string(got), "connectTimeout := 60") {
		t.Errorf("the edit did not land:\n%s", got)
	}

	// main must be untouched: the agent worked on a branch.
	onMain, err := api.ReadFile(context.Background(), s, "main", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onMain), "60") {
		t.Error("the commit leaked onto main")
	}
}

// TestInitializeNegotiatesProtocolVersion checks both directions of version
// negotiation: a version we speak is echoed, and one we do not is answered
// with ours rather than accepted silently.
func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	c, _ := fixture(t)

	for _, tc := range []struct{ asked, want string }{
		{"2024-11-05", "2024-11-05"},
		{"2025-06-18", "2025-06-18"},
		{"1999-01-01", "2025-06-18"},
		{"", "2025-06-18"},
	} {
		sess := newSession(t, c)
		reply := sess.call("initialize", map[string]any{"protocolVersion": tc.asked})
		res, ok := reply["result"].(map[string]any)
		if !ok {
			t.Fatalf("asked %q: no result: %v", tc.asked, reply)
		}
		if got := res["protocolVersion"]; got != tc.want {
			t.Errorf("asked %q: server answered %v, want %s", tc.asked, got, tc.want)
		}
		caps, _ := res["capabilities"].(map[string]any)
		if _, ok := caps["tools"]; !ok {
			t.Errorf("asked %q: server did not advertise tools: %v", tc.asked, caps)
		}
	}
}

// TestToolsListIsUsableByAModel checks the part a client actually consumes.
// A tool whose schema is missing or whose required list names a property that
// does not exist will be called wrongly or not at all, and that failure shows
// up as the agent ignoring perry rather than as an error.
func TestToolsListIsUsableByAModel(t *testing.T) {
	c, _ := fixture(t)
	sess := newSession(t, c)
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	res, ok := sess.call("tools/list", nil)["result"].(map[string]any)
	if !ok {
		t.Fatal("tools/list returned no result")
	}
	tools, _ := res["tools"].([]any)
	if len(tools) < 6 {
		t.Fatalf("expected the full tool set, got %d", len(tools))
	}

	seen := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" {
			t.Errorf("a tool has no name: %v", tool)
			continue
		}
		seen[name] = true

		if desc, _ := tool["description"].(string); len(desc) < 20 {
			t.Errorf("%s: description is too thin for a model to choose on: %q", name, desc)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("%s: no inputSchema", name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: inputSchema type is %v, want object", name, schema["type"])
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: inputSchema has no properties", name)
			continue
		}
		for _, r := range toStrings(schema["required"]) {
			if _, ok := props[r]; !ok {
				t.Errorf("%s: required property %q is not declared", name, r)
			}
		}
	}

	for _, want := range []string{
		"perry_grep", "perry_read_file", "perry_list_files",
		"perry_list_tree", "perry_list_branches", "perry_create_branch", "perry_commit",
	} {
		if !seen[want] {
			t.Errorf("tool %s is missing", want)
		}
	}
}

func toStrings(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestToolFailureReachesTheModel pins down an easy mistake: reporting a failed
// tool call as a JSON-RPC error. The model never sees a protocol error, so a
// missing file would look to it like the tool had vanished. It has to come
// back as a successful call whose content says what went wrong.
func TestToolFailureReachesTheModel(t *testing.T) {
	c, _ := fixture(t)
	sess := newSession(t, c)
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"missing file", "perry_read_file", map[string]any{"path": "does/not/exist.go"}, "not found"},
		{"bad regex", "perry_grep", map[string]any{"pattern": "([unclosed"}, "perry:"},
		{"no pattern", "perry_grep", map[string]any{}, "pattern is required"},
		{"commit with no changes", "perry_commit", map[string]any{"branch": "main", "message": "m", "changes": []any{}}, "at least one file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, isErr := sess.tool(tc.tool, tc.args)
			if !isErr {
				t.Fatalf("expected an error result, got: %s", out)
			}
			if !strings.Contains(strings.ToLower(out), strings.ToLower(tc.want)) {
				t.Errorf("error text %q does not mention %q", out, tc.want)
			}
		})
	}
}

// TestCommitRefusesAStaleExpect is the concurrency guarantee, seen from the
// agent's side: two agents editing one branch must not silently overwrite
// each other, and the loser must be told clearly enough to re-read and retry.
func TestCommitRefusesAStaleExpect(t *testing.T) {
	c, s := fixture(t)
	sess := newSession(t, c)
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	// What the agent saw when it started.
	before, isErr := sess.tool("perry_list_branches", nil)
	if isErr {
		t.Fatalf("branches failed: %s", before)
	}
	fields := strings.Fields(before)
	if len(fields) < 2 {
		t.Fatalf("unexpected branch listing: %q", before)
	}
	stale := fields[0]

	// Another writer moves main underneath it.
	if _, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch:  "main",
		Message: "somebody else got there first",
		Author:  "Other", Email: "other@example.com",
		Changes: []api.Change{{Path: "README.md", Content: []byte("# Demo\n\nChanged.\n")}},
	}); err != nil {
		t.Fatal(err)
	}

	out, isErr := sess.tool("perry_commit", map[string]any{
		"branch":  "main",
		"message": "based on a stale read",
		"expect":  stale,
		"changes": []map[string]any{{"path": "README.md", "content": "# Demo\n\nMine.\n"}},
	})
	if !isErr {
		t.Fatalf("stale commit was accepted: %s", out)
	}

	// The other writer's content must still be there.
	got, err := api.ReadFile(context.Background(), s, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Changed.") {
		t.Errorf("the rejected commit overwrote the other writer anyway:\n%s", got)
	}
}

// TestDeleteIsDistinctFromAnEmptyFile guards the nil-versus-empty distinction
// that the adapter has to preserve when it converts a model's JSON string
// into perry's Change.Content.
func TestDeleteIsDistinctFromAnEmptyFile(t *testing.T) {
	c, s := fixture(t)
	sess := newSession(t, c)
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	out, isErr := sess.tool("perry_commit", map[string]any{
		"branch":  "main",
		"message": "empty one file, remove another",
		"changes": []map[string]any{
			{"path": "README.md", "content": ""},
			{"path": "src/pkg/util.go", "delete": true},
		},
	})
	if isErr {
		t.Fatalf("commit failed: %s", out)
	}

	ctx := context.Background()
	got, err := api.ReadFile(ctx, s, "main", "README.md")
	if err != nil {
		t.Fatalf("the emptied file was removed instead of emptied: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("README.md should be empty, got %q", got)
	}
	if _, err := api.ReadFile(ctx, s, "main", "src/pkg/util.go"); err == nil {
		t.Error("the deleted file is still present")
	}
}

// TestNotificationsAndBadFramesDoNotDesyncTheStream checks that the adapter
// keeps JSON-RPC's rules about replies. A reply to a notification, or a
// missing reply to a request, shifts every later response by one and the
// client sees another call's answer.
func TestNotificationsAndBadFramesDoNotDesyncTheStream(t *testing.T) {
	c, _ := fixture(t)
	sess := newSession(t, c)
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	// Notifications draw nothing, including the unknown one.
	sess.notify("notifications/initialized")
	sess.notify("notifications/cancelled")
	sess.notify("some/unknown/notification")
	// A blank line is skipped rather than answered.
	sess.sendRaw("")

	// A malformed frame does get an error, with a null id since there is
	// none to echo.
	sess.sendRaw("{not json")
	reply := sess.read()
	errObj, ok := reply["error"].(map[string]any)
	if !ok {
		t.Fatalf("malformed JSON was not reported as an error: %v", reply)
	}
	if code, _ := errObj["code"].(float64); int(code) != -32700 {
		t.Errorf("parse error code is %v, want -32700", errObj["code"])
	}

	// An unknown method on a request is a method-not-found error.
	reply = sess.call("tools/nonexistent", nil)
	errObj, ok = reply["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown method was not reported as an error: %v", reply)
	}
	if code, _ := errObj["code"].(float64); int(code) != -32601 {
		t.Errorf("method-not-found code is %v, want -32601", errObj["code"])
	}

	// The stream is still aligned: this reply must carry this request's id.
	reply = sess.call("ping", nil)
	if got, want := reply["id"], float64(sess.id); got != want {
		t.Fatalf("stream desynced: reply id %v, want %v", got, want)
	}
}

// TestServerSurvivesAnUnreachableBackend covers the realistic misconfiguration
// where an agent launches the adapter before the perry server is up. The
// adapter must keep answering tool calls with an explanation rather than
// exiting, since an MCP client that loses its server loses the tools.
func TestServerSurvivesAnUnreachableBackend(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	url := ts.URL
	ts.Close() // nothing is listening now

	sess := newSession(t, mcp.NewClient(url, "demo", ""))
	sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})

	out, isErr := sess.tool("perry_grep", map[string]any{"pattern": "anything"})
	if !isErr {
		t.Fatalf("expected a failure against a dead server, got: %s", out)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("error does not say the server is unreachable: %q", out)
	}

	// Still alive and still serving tools.
	if reply := sess.call("ping", nil); reply["error"] != nil {
		t.Errorf("the adapter stopped working after a backend failure: %v", reply["error"])
	}
}

// TestTokenIsSentOnEveryCall is the adapter half of the authentication fix: a
// server started with -token must be reachable through MCP, and a wrong token
// must fail loudly rather than appear as an empty repository.
func TestTokenIsSentOnEveryCall(t *testing.T) {
	const token = "s3cret"

	s, err := sqlite.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := api.Commit(context.Background(), s, api.CommitRequest{
		Branch: "main", Message: "seed", Author: "F", Email: "f@example.com",
		Changes: []api.Change{{Path: "a.txt", Content: []byte("needle\n")}},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", &api.Handler{Open: func(string) (store.Store, error) { return s, nil }})
	ts := httptest.NewServer(api.RequireToken(mux, token))
	t.Cleanup(ts.Close)

	t.Run("with the token", func(t *testing.T) {
		sess := newSession(t, mcp.NewClient(ts.URL, "demo", token))
		sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
		out, isErr := sess.tool("perry_grep", map[string]any{"pattern": "needle"})
		if isErr {
			t.Fatalf("authenticated grep failed: %s", out)
		}
		if !strings.Contains(out, "a.txt:1:") {
			t.Errorf("grep found nothing:\n%s", out)
		}
	})

	t.Run("with the wrong token", func(t *testing.T) {
		sess := newSession(t, mcp.NewClient(ts.URL, "demo", "wrong"))
		sess.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
		out, isErr := sess.tool("perry_grep", map[string]any{"pattern": "needle"})
		if !isErr {
			t.Fatalf("a wrong token was accepted: %s", out)
		}
		if !strings.Contains(out, "401") {
			t.Errorf("error does not identify an authentication failure: %q", out)
		}
	})
}
