// Package server exposes repositories over git's smart HTTP transport.
package server

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/FloareDor/perry/internal/protocol"
	"github.com/FloareDor/perry/internal/store"
)

// Opener creates or opens the store backing a named repository.
type Opener func(name string) (store.Store, error)

// Server routes git smart HTTP requests to per-repository stores.
type Server struct {
	open   Opener
	Logger *log.Logger

	// AutoCreate makes a push to an unknown repository create it. It is
	// convenient for agent fleets, which spin repositories up and throw
	// them away, and wrong for a shared server, so it is off by default.
	AutoCreate bool

	mu    sync.Mutex
	repos map[string]store.Store
}

// New returns a server that opens repositories through open.
func New(open Opener) *Server {
	return &Server{
		open:   open,
		repos:  make(map[string]store.Store),
		Logger: log.Default(),
	}
}

// Repo returns the store for a repository, opening it on first use.
func (s *Server) Repo(name string) (store.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.repos[name]; ok {
		return st, nil
	}
	st, err := s.open(name)
	if err != nil {
		return nil, err
	}
	s.repos[name] = st
	return st, nil
}

// Close releases every open repository.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, st := range s.repos {
		if err := st.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.repos = make(map[string]store.Store)
	return firstErr
}

// ServeHTTP routes the three smart HTTP endpoints.
//
// Paths look like /<repo>.git/info/refs. The repository name is everything
// before the endpoint suffix, so nested names such as org/project work
// without any extra routing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs"):
		s.handleInfoRefs(w, r, repoName(path, "/info/refs"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/git-upload-pack"):
		s.handleUploadPack(w, r, repoName(path, "/git-upload-pack"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/git-receive-pack"):
		s.handleReceivePack(w, r, repoName(path, "/git-receive-pack"))
	default:
		http.NotFound(w, r)
	}
}

func repoName(path, suffix string) string {
	return strings.TrimSuffix(strings.TrimSuffix(path, suffix), ".git")
}

func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, repo string) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		// A client that asks for no service wants the dumb protocol, which
		// perry does not serve.
		http.Error(w, "perry serves the smart HTTP protocol only", http.StatusForbidden)
		return
	}

	st, err := s.Repo(repo)
	if err != nil {
		s.fail(w, repo, err, http.StatusNotFound)
		return
	}

	refs, err := st.ListRefs(r.Context(), "refs/")
	if err != nil {
		s.fail(w, repo, err, http.StatusInternalServerError)
		return
	}
	head, err := st.HeadTarget(r.Context())
	if err != nil {
		s.fail(w, repo, err, http.StatusInternalServerError)
		return
	}

	setGitHeaders(w, "application/x-"+service+"-advertisement")
	if err := protocol.AdvertiseRefs(w, service, refs, head); err != nil {
		s.logf("%s: advertise: %v", repo, err)
	}
}

func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, repo string) {
	st, err := s.Repo(repo)
	if err != nil {
		s.fail(w, repo, err, http.StatusNotFound)
		return
	}

	body, err := requestBody(r)
	if err != nil {
		s.fail(w, repo, err, http.StatusBadRequest)
		return
	}
	defer body.Close()

	req, err := protocol.ParseUploadRequest(body)
	if err != nil {
		s.fail(w, repo, err, http.StatusBadRequest)
		return
	}

	// Headers must go out before the pack starts, because once the body is
	// flowing the status code can no longer be changed.
	setGitHeaders(w, "application/x-git-upload-pack-result")
	if err := protocol.UploadPack(r.Context(), st, req, w); err != nil {
		s.logf("%s: upload-pack: %v", repo, err)
	}
}

func (s *Server) handleReceivePack(w http.ResponseWriter, r *http.Request, repo string) {
	st, err := s.Repo(repo)
	if err != nil {
		if !s.AutoCreate {
			s.fail(w, repo, err, http.StatusNotFound)
			return
		}
		s.fail(w, repo, err, http.StatusNotFound)
		return
	}

	body, err := requestBody(r)
	if err != nil {
		s.fail(w, repo, err, http.StatusBadRequest)
		return
	}
	defer body.Close()

	req, err := protocol.ParsePushRequest(body)
	if err != nil {
		s.fail(w, repo, err, http.StatusBadRequest)
		return
	}

	setGitHeaders(w, "application/x-git-receive-pack-result")
	if err := protocol.ReceivePack(r.Context(), st, req, w); err != nil {
		s.logf("%s: receive-pack: %v", repo, err)
	}
}

// requestBody unwraps gzip, which git applies to larger request bodies.
func requestBody(r *http.Request) (io.ReadCloser, error) {
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip request body: %w", err)
		}
		return zr, nil
	}
	return r.Body, nil
}

// setGitHeaders writes the content type and the no-cache headers git's HTTP
// transport expects. Without them an intermediate proxy can serve a stale
// reference advertisement, and the client then asks for objects that have
// since been garbage collected.
func setGitHeaders(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
}

func (s *Server) fail(w http.ResponseWriter, repo string, err error, code int) {
	s.logf("%s: %v", repo, err)
	http.Error(w, http.StatusText(code), code)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}
