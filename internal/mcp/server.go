package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Protocol versions this adapter implements. MCP negotiates a version at
// initialize time: the client proposes one, and the server answers with the
// version it will actually speak. If the client's proposal is one we know,
// we echo it; otherwise we answer with our newest and let the client decide
// whether it can live with that, which is what the specification asks for.
const latestProtocolVersion = "2025-06-18"

var knownProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// ServerName and ServerVersion identify this adapter to the client.
const (
	ServerName    = "perry"
	ServerVersion = "0.1.0"
)

// request is an incoming JSON-RPC 2.0 message.
//
// ID is kept as a raw message because JSON-RPC permits a string or a number
// and the reply must echo the exact form it arrived in. Decoding it into a
// Go type and re-encoding would turn 1 into 1.0 for some clients. An absent
// ID means a notification, which takes no reply.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// nullID is the literal JSON null, which a reply uses when the request
// carried no usable id.
var nullID = json.RawMessage("null")

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Tool is one callable exposed to the agent.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`

	// Handler returns the text the model sees. An error is reported as a
	// tool error rather than a protocol error, so the model can read what
	// went wrong and try something else instead of the conversation dying.
	Handler func(ctx context.Context, args json.RawMessage) (string, error) `json:"-"`
}

// Server dispatches MCP requests over a stdio pipe.
type Server struct {
	tools []Tool
	index map[string]*Tool

	// Log receives diagnostics. It must not be stdout: on the stdio
	// transport stdout carries protocol frames, and a stray log line there
	// is a parse error on the client.
	Log func(format string, args ...any)

	mu  sync.Mutex
	out *bufio.Writer

	initialized bool
}

// NewServer builds a server over a tool set.
func NewServer(tools []Tool) *Server {
	s := &Server{tools: tools, index: make(map[string]*Tool, len(tools))}
	for i := range s.tools {
		s.index[s.tools[i].Name] = &s.tools[i]
	}
	return s
}

// Serve reads newline-delimited JSON-RPC from in and writes replies to out,
// returning when in reaches EOF.
//
// MCP's stdio transport frames messages one per line, so the loop is a
// scanner rather than a Content-Length parser. The buffer is raised because
// a tools/call result can carry a whole source file.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = bufio.NewWriter(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// The line is handled synchronously. MCP allows concurrent
		// requests, but an agent's tool calls arrive one at a time and
		// serial handling keeps the ordering obvious; a parallel
		// dispatcher here would buy nothing and risk interleaved writes.
		s.handle(ctx, line)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		// A frame too broken to parse has no id to echo, but JSON-RPC still
		// requires a reply, with a null id. Dropping it instead would leave
		// a client that had sent a request waiting for a response forever.
		s.writeError(nullID, codeParse, "malformed JSON")
		return
	}
	if req.JSONRPC != "2.0" {
		s.writeError(req.ID, codeInvalidRequest, `"jsonrpc" must be "2.0"`)
		return
	}

	// A notification has no ID and takes no reply, not even an error.
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.initialized = true
		s.reply(req.ID, s.initializeResult(req.Params))
	case "notifications/initialized", "notifications/cancelled":
		// Nothing to do, and nothing to answer.
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": s.tools})
	case "tools/call":
		s.callTool(ctx, req)
	default:
		if notification {
			return
		}
		s.writeError(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
	}
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := latestProtocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && knownProtocolVersions[p.ProtocolVersion] {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
	}
}

func (s *Server) callTool(ctx context.Context, req request) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(req.ID, codeInvalidParams, "params must be {name, arguments}")
		return
	}
	tool, ok := s.index[p.Name]
	if !ok {
		s.writeError(req.ID, codeInvalidParams, "unknown tool: "+p.Name)
		return
	}

	text, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		if s.Log != nil {
			s.Log("tool %s failed: %v", p.Name, err)
		}
		// Reported as a successful call carrying an error, per MCP: the
		// model is the one that has to react to "no such file", and it
		// cannot see a transport-level error.
		s.reply(req.ID, toolResult(err.Error(), true))
		return
	}
	s.reply(req.ID, toolResult(text, false))
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *Server) reply(id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		// A notification gets no reply even when it is bad, so the only
		// place to record it is the log.
		if s.Log != nil {
			s.Log("dropping bad notification: %s", msg)
		}
		return
	}
	s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *Server) write(r response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(r)
	if err != nil {
		data, _ = json.Marshal(response{
			JSONRPC: "2.0", ID: r.ID,
			Error: &rpcError{Code: codeInternal, Message: "result could not be encoded"},
		})
	}
	// One frame per line, so the newline is part of the protocol and the
	// flush has to happen now rather than when the buffer fills.
	s.out.Write(data)
	s.out.WriteByte('\n')
	if err := s.out.Flush(); err != nil && s.Log != nil {
		s.Log("write: %v", err)
	}
}
