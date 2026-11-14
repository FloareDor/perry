// Command perry runs a git server for coding agents.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/FloareDor/perry/internal/api"
	"github.com/FloareDor/perry/internal/mcp"
	"github.com/FloareDor/perry/internal/server"
	"github.com/FloareDor/perry/internal/store"
	"github.com/FloareDor/perry/internal/store/sqlite"
)

const usage = `perry: a git server for coding agents

Usage:
  perry serve [flags]        Run the server
  perry mcp [flags]          Bridge a repository to a coding agent over MCP
  perry grep <repo> <regex>  Search a repository on disk
  perry ls <repo> [glob]     List files at HEAD
  perry refs <repo>          List references
  perry version

Serve flags:
  -addr      string  listen address (default "127.0.0.1:8080")
  -data      string  directory holding repository databases (default "./data")
  -token     string  required on every route, as a bearer token or as the
                     password in HTTP Basic auth (default none)
  -insecure  bool    serve a non-loopback address without a token

Git smart HTTP carries receive-pack, so a perry server without -token accepts
anonymous pushes to any repository. The default address is loopback, and
serving a public address without a token is refused unless -insecure is given.

MCP flags:
  -server  string  perry server URL (default "http://127.0.0.1:8080")
  -repo    string  repository to expose (required)
  -token   string  bearer token (default $PERRY_TOKEN)
  -author  string  commit author name
  -email   string  commit author email

perry mcp speaks the Model Context Protocol on stdin and stdout, so a coding
agent configured to run it gets repository search, file reads and commits
against a remote perry without cloning anything.

Examples:
  perry serve -data ./data
  perry serve -addr 0.0.0.0:8080 -token "$PERRY_TOKEN"
  git clone http://localhost:8080/demo.git
  git clone http://x:"$PERRY_TOKEN"@host:8080/demo.git
  curl 'http://localhost:8080/v1/repos/demo/grep?q=func+main'
  curl 'http://localhost:8080/v1/repos/demo/file?ref=main&path=README.md'
  perry mcp -server http://perry.internal:8080 -repo demo
`

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "mcp":
		runMCP(os.Args[2:])
	case "grep":
		grep(os.Args[2:])
	case "ls":
		list(os.Args[2:])
	case "refs":
		refs(os.Args[2:])
	case "version":
		fmt.Println("perry 0.1.0")
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "perry: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// repoManager opens repository databases on demand and keeps them open.
type repoManager struct {
	dir string

	mu     sync.Mutex
	stores map[string]store.Store
}

func newRepoManager(dir string) *repoManager {
	return &repoManager{dir: dir, stores: make(map[string]store.Store)}
}

// open returns the store for a repository, creating the database on first
// use. A push to an unknown name creates it, which is what an agent fleet
// spinning up throwaway repositories needs.
func (m *repoManager) open(name string) (store.Store, error) {
	clean := sanitizeRepoName(name)
	if clean == "" {
		return nil, fmt.Errorf("invalid repository name %q", name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.stores[clean]; ok {
		return s, nil
	}

	path := filepath.Join(m.dir, clean+".db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s, err := sqlite.Open(path)
	if err != nil {
		return nil, err
	}
	m.stores[clean] = s
	return s, nil
}

func (m *repoManager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.stores {
		_ = s.Close()
	}
}

// sanitizeRepoName keeps a request path from escaping the data directory.
// A name may contain slashes, so that org/project works, but never a
// parent reference, an absolute path or a drive letter.
func sanitizeRepoName(name string) string {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") {
		return ""
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return name
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// Loopback by default. A perry server with no token accepts anonymous
	// pushes to any repository and creates repositories on demand, so binding
	// every interface by default would make "perry serve" on a reachable host
	// world-writable. Exposing it is a deliberate act: pass an explicit
	// address, and a token to go with it.
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	dir := fs.String("data", "./data", "directory holding repository databases")
	token := fs.String("token", "", "token required on every route, as a bearer token or as the password in HTTP Basic auth")
	insecure := fs.Bool("insecure", false, "serve a non-loopback address without a token (do not use on a public network)")
	_ = fs.Parse(args)

	if *token == "" && !isLoopback(*addr) && !*insecure {
		log.Fatalf("refusing to serve %s without -token: git smart HTTP carries receive-pack, "+
			"so an unauthenticated perry accepts anonymous pushes to any repository and "+
			"creates repositories on demand.\n"+
			"  set a token:        perry serve -addr %s -token $PERRY_TOKEN\n"+
			"  or keep it local:   perry serve\n"+
			"  or accept the risk: perry serve -addr %s -insecure",
			*addr, *addr, *addr)
	}

	mgr := newRepoManager(*dir)
	defer mgr.closeAll()

	gitSrv := server.New(mgr.open)
	// The token is enforced by the middleware below, which covers the git
	// routes too, so the API handler does not check it a second time.
	readAPI := &api.Handler{Open: mgr.open}

	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", readAPI)
	mux.Handle("/", gitSrv)

	// Health checks stay open: a liveness probe should not need a credential,
	// and it reveals nothing but that the process is up.
	outer := http.NewServeMux()
	outer.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	outer.Handle("/", api.RequireToken(mux, *token))

	srv := &http.Server{
		Addr:    *addr,
		Handler: logRequests(outer),
		// A clone of a large repository legitimately takes a long time, so
		// the write timeout is generous. The read header timeout stays
		// short, since a client that cannot send headers promptly is not
		// going to finish a fetch.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("perry listening on %s, data in %s", *addr, *dir)
	if *token == "" {
		log.Printf("  no -token set: every route is open to anyone who can reach this address")
	}
	log.Printf("  git clone http://%s/demo.git", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// runMCP bridges one repository to a coding agent over the Model Context
// Protocol.
//
// The transport is stdin and stdout, which is what every MCP client launches
// a local server as, so connecting an existing agent is a few lines of its
// configuration rather than a change to how the developer works. Logging goes
// to stderr: stdout carries protocol frames, and one stray line there is a
// parse error at the other end.
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	server := fs.String("server", "http://127.0.0.1:8080", "perry server URL")
	repo := fs.String("repo", "", "repository to expose")
	token := fs.String("token", os.Getenv("PERRY_TOKEN"), "bearer token, defaults to $PERRY_TOKEN")
	author := fs.String("author", "perry-agent", "commit author name")
	email := fs.String("email", "agent@perry.local", "commit author email")
	_ = fs.Parse(args)

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "perry mcp: -repo is required")
		os.Exit(2)
	}

	client := mcp.NewClient(*server, *repo, *token)
	client.Author, client.Email = *author, *email

	srv := mcp.NewServer(mcp.Tools(client))
	srv.Log = func(format string, a ...any) { log.Printf("mcp: "+format, a...) }

	log.SetOutput(os.Stderr)
	log.Printf("mcp: serving %s from %s on stdio", *repo, *server)
	if err := srv.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		log.Fatalf("perry mcp: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %v", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func openForCLI(repoArg string) (store.Store, func()) {
	path := repoArg
	if filepath.Ext(path) != ".db" {
		path += ".db"
	}
	s, err := sqlite.Open(path)
	if err != nil {
		log.Fatalf("perry: open %s: %v", path, err)
	}
	return s, func() { _ = s.Close() }
}

func grep(args []string) {
	if len(args) < 2 {
		log.Fatal("usage: perry grep <repo.db> <regex> [ref]")
	}
	s, done := openForCLI(args[0])
	defer done()

	opts := api.GrepOptions{Pattern: args[1], Limit: 200}
	if len(args) > 2 {
		opts.Rev = args[2]
	}
	res, err := api.Grep(context.Background(), s, opts)
	if err != nil {
		log.Fatalf("perry: %v", err)
	}
	for _, m := range res.Matches {
		fmt.Printf("%s:%d: %s\n", m.Path, m.Line, m.Text)
	}
	if res.Truncated {
		fmt.Fprintln(os.Stderr, "(truncated)")
	}
}

func list(args []string) {
	if len(args) < 1 {
		log.Fatal("usage: perry ls <repo.db> [glob]")
	}
	s, done := openForCLI(args[0])
	defer done()

	glob := ""
	if len(args) > 1 {
		glob = args[1]
	}
	paths, err := api.ListFiles(context.Background(), s, "HEAD", glob)
	if err != nil {
		log.Fatalf("perry: %v", err)
	}
	for _, p := range paths {
		fmt.Println(p)
	}
}

func refs(args []string) {
	if len(args) < 1 {
		log.Fatal("usage: perry refs <repo.db>")
	}
	s, done := openForCLI(args[0])
	defer done()

	list, err := s.ListRefs(context.Background(), "")
	if err != nil {
		log.Fatalf("perry: %v", err)
	}
	out := make([]map[string]string, 0, len(list))
	for _, r := range list {
		out = append(out, map[string]string{"name": r.Name, "target": r.Target.String()})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// isLoopback reports whether a listen address is reachable only from this
// machine. An empty host means every interface, which is not loopback however
// the port is written.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost":
		return true
	case "", "0.0.0.0", "::", "[::]":
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
