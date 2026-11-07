// Command perry runs a git server for coding agents.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/perry-dev/perry/internal/api"
	"github.com/perry-dev/perry/internal/server"
	"github.com/perry-dev/perry/internal/store"
	"github.com/perry-dev/perry/internal/store/sqlite"
)

const usage = `perry: a git server for coding agents

Usage:
  perry serve [flags]        Run the server
  perry grep <repo> <regex>  Search a repository on disk
  perry ls <repo> [glob]     List files at HEAD
  perry refs <repo>          List references
  perry version

Serve flags:
  -addr   string  listen address (default ":8080")
  -data   string  directory holding repository databases (default "./data")
  -token  string  bearer token required on the read API (default none)

Examples:
  perry serve -data ./data -addr :8080
  git clone http://localhost:8080/demo.git
  curl 'http://localhost:8080/v1/repos/demo/grep?q=func+main'
  curl 'http://localhost:8080/v1/repos/demo/file?ref=main&path=README.md'
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
	addr := fs.String("addr", ":8080", "listen address")
	dir := fs.String("data", "./data", "directory holding repository databases")
	token := fs.String("token", "", "bearer token required on the read API")
	_ = fs.Parse(args)

	mgr := newRepoManager(*dir)
	defer mgr.closeAll()

	gitSrv := server.New(mgr.open)
	readAPI := &api.Handler{Open: mgr.open, Token: *token}

	mux := http.NewServeMux()
	mux.Handle("/v1/repos/", readAPI)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.Handle("/", gitSrv)

	srv := &http.Server{
		Addr:    *addr,
		Handler: logRequests(mux),
		// A clone of a large repository legitimately takes a long time, so
		// the write timeout is generous. The read header timeout stays
		// short, since a client that cannot send headers promptly is not
		// going to finish a fetch.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("perry listening on %s, data in %s", *addr, *dir)
	log.Printf("  git clone http://localhost%s/demo.git", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
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
