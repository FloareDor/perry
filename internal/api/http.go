package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/perry-dev/perry/internal/store"
)

// Handler serves the read API under /v1/repos/{repo}/...
type Handler struct {
	Open func(repo string) (store.Store, error)
	// Token, when set, is required as a bearer token on every request.
	Token string
}

// ServeHTTP routes the read endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Token != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		// A plain comparison is fine here because the token is compared in
		// full and the endpoint is not a login oracle, but constant time
		// costs nothing and removes the question.
		if subtleCompare(got, h.Token) == false {
			writeError(w, http.StatusUnauthorized, "bad or missing bearer token")
			return
		}
	}

	rest := strings.TrimPrefix(r.URL.Path, "/v1/repos/")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		writeError(w, http.StatusNotFound, "expected /v1/repos/{repo}/{endpoint}")
		return
	}
	repo, endpoint := rest[:slash], rest[slash+1:]

	s, err := h.Open(repo)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such repository: "+repo)
		return
	}

	switch {
	case endpoint == "grep" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		h.grep(w, r, s)
	case endpoint == "file" && r.Method == http.MethodGet:
		h.file(w, r, s)
	case endpoint == "tree" && r.Method == http.MethodGet:
		h.tree(w, r, s)
	case endpoint == "files" && r.Method == http.MethodGet:
		h.files(w, r, s)
	case endpoint == "refs" && r.Method == http.MethodGet:
		h.refs(w, r, s)
	case endpoint == "fork" && r.Method == http.MethodPost:
		h.fork(w, r, s)
	case endpoint == "commit" && r.Method == http.MethodPost:
		h.commit(w, r, s)
	case endpoint == "ref" && r.Method == http.MethodPost:
		h.updateRef(w, r, s)
	default:
		writeError(w, http.StatusNotFound, "unknown endpoint: "+endpoint)
	}
}

func (h *Handler) grep(w http.ResponseWriter, r *http.Request, s store.Store) {
	opts := GrepOptions{
		Pattern:       r.URL.Query().Get("q"),
		Rev:           r.URL.Query().Get("ref"),
		CaseSensitive: r.URL.Query().Get("case") == "true",
	}
	if p := r.URL.Query()["path"]; len(p) > 0 {
		opts.PathPrefixes = p
	}
	opts.Context, _ = strconv.Atoi(r.URL.Query().Get("context"))
	opts.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))

	// A POST body may carry the same options, which keeps a long regex off
	// the URL and out of request logs.
	if r.Method == http.MethodPost && r.Body != nil {
		var body GrepOptions
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.Pattern != "" {
				opts.Pattern = body.Pattern
			}
			if body.Rev != "" {
				opts.Rev = body.Rev
			}
			if len(body.PathPrefixes) > 0 {
				opts.PathPrefixes = body.PathPrefixes
			}
			if body.Context > 0 {
				opts.Context = body.Context
			}
			if body.Limit > 0 {
				opts.Limit = body.Limit
			}
			opts.CaseSensitive = opts.CaseSensitive || body.CaseSensitive
		}
	}

	res, err := Grep(r.Context(), s, opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request, s store.Store) {
	q := r.URL.Query()
	data, err := ReadFile(r.Context(), s, q.Get("ref"), q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Served as bytes rather than JSON so a large file streams without a
	// base64 round trip, and so the client can diff it directly.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (h *Handler) tree(w http.ResponseWriter, r *http.Request, s store.Store) {
	q := r.URL.Query()
	entries, err := ListTree(r.Context(), s, q.Get("ref"), q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func (h *Handler) files(w http.ResponseWriter, r *http.Request, s store.Store) {
	q := r.URL.Query()
	paths, err := ListFiles(r.Context(), s, q.Get("ref"), q.Get("glob"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"paths": paths, "count": len(paths)})
}

func (h *Handler) refs(w http.ResponseWriter, r *http.Request, s store.Store) {
	refs, err := s.ListRefs(r.Context(), r.URL.Query().Get("prefix"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, map[string]string{"name": ref.Name, "target": ref.Target.String()})
	}
	writeJSON(w, map[string]any{"refs": out})
}

func (h *Handler) fork(w http.ResponseWriter, r *http.Request, s store.Store) {
	var body struct {
		From   string `json:"from"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be {\"from\":..., \"branch\":...}")
		return
	}
	if body.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	target, err := Fork(r.Context(), s, body.From, body.Branch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"branch": body.Branch, "target": target.String()})
}

func (h *Handler) commit(w http.ResponseWriter, r *http.Request, s store.Store) {
	var req CommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed commit request: "+err.Error())
		return
	}
	res, err := Commit(r.Context(), s, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (h *Handler) updateRef(w http.ResponseWriter, r *http.Request, s store.Store) {
	var req UpdateRefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed ref update: "+err.Error())
		return
	}
	res, err := UpdateRef(r.Context(), s, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeErr maps a store error onto the right status code.
func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var conflict *store.ErrRefConflict
	if errors.As(err, &conflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// subtleCompare compares two strings without leaking their common prefix
// length through timing.
func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
