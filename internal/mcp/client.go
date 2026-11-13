// Package mcp exposes a perry repository to a coding agent over the Model
// Context Protocol.
//
// The adapter is a client of perry's HTTP API rather than of the object store
// directly. That is the whole point: an agent runs this on its own machine,
// points it at a perry server that may be on another host, and gets code
// search, file reads and commits without ever cloning. Opening the SQLite
// file locally would work for a single-machine demo and would quietly throw
// away the property worth demonstrating.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/perry-dev/perry/internal/api"
)

// Client speaks perry's read/write HTTP API for one repository.
type Client struct {
	// Base is the server root, e.g. http://perry.internal:8080
	Base string
	// Repo is the repository name the tools operate on.
	Repo string
	// Token, when set, is sent as a bearer token.
	Token string

	// Author and Email fill in a commit whose caller left them out. A model
	// asked to supply an author will invent one, so the operator sets it
	// once at the command line instead.
	Author, Email string

	HTTP *http.Client
}

// NewClient returns a client with sensible timeouts.
//
// The timeout is generous because a grep over a large repository is a real
// scan, not a cache hit, and an agent would rather wait than retry.
func NewClient(base, repo, token string) *Client {
	return &Client{
		Base:  strings.TrimRight(base, "/"),
		Repo:  strings.Trim(repo, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) endpoint(name string, q url.Values) string {
	u := c.Base + "/v1/repos/" + c.Repo + "/" + name
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// do issues a request and returns the body, turning a perry error document
// into a Go error so a tool handler does not have to unpack it.
func (c *Client) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perry server unreachable at %s: %w", c.Base, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("perry: %s (HTTP %d)", e.Error, resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("perry: unauthorized (HTTP 401): set -token, or PERRY_TOKEN")
		}
		return nil, fmt.Errorf("perry: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Grep searches the repository at a revision.
func (c *Client) Grep(ctx context.Context, pattern, rev string, paths []string, ctxLines, limit int, caseSensitive bool) (*api.GrepResult, error) {
	q := url.Values{"q": {pattern}}
	if rev != "" {
		q.Set("ref", rev)
	}
	for _, p := range paths {
		q.Add("path", p)
	}
	if ctxLines > 0 {
		q.Set("context", strconv.Itoa(ctxLines))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if caseSensitive {
		q.Set("case", "true")
	}
	data, err := c.do(ctx, http.MethodGet, c.endpoint("grep", q), nil)
	if err != nil {
		return nil, err
	}
	var res api.GrepResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("perry: malformed grep response: %w", err)
	}
	return &res, nil
}

// ReadFile returns one file's bytes at a revision.
func (c *Client) ReadFile(ctx context.Context, rev, path string) ([]byte, error) {
	q := url.Values{"path": {path}}
	if rev != "" {
		q.Set("ref", rev)
	}
	return c.do(ctx, http.MethodGet, c.endpoint("file", q), nil)
}

// ListFiles returns the paths at a revision, optionally filtered by a glob.
func (c *Client) ListFiles(ctx context.Context, rev, glob string) ([]string, error) {
	q := url.Values{}
	if rev != "" {
		q.Set("ref", rev)
	}
	if glob != "" {
		q.Set("glob", glob)
	}
	data, err := c.do(ctx, http.MethodGet, c.endpoint("files", q), nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res.Paths, nil
}

// ListTree returns the entries directly under a directory.
func (c *Client) ListTree(ctx context.Context, rev, dir string) ([]api.Entry, error) {
	q := url.Values{}
	if rev != "" {
		q.Set("ref", rev)
	}
	if dir != "" {
		q.Set("path", dir)
	}
	data, err := c.do(ctx, http.MethodGet, c.endpoint("tree", q), nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Entries []api.Entry `json:"entries"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res.Entries, nil
}

// Refs lists the repository's references.
func (c *Client) Refs(ctx context.Context, prefix string) ([]map[string]string, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	data, err := c.do(ctx, http.MethodGet, c.endpoint("refs", q), nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Refs []map[string]string `json:"refs"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res.Refs, nil
}

// Commit writes a commit without a working tree.
func (c *Client) Commit(ctx context.Context, req api.CommitRequest) (*api.CommitResult, error) {
	if req.Author == "" {
		req.Author = c.Author
	}
	if req.Email == "" {
		req.Email = c.Email
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := c.do(ctx, http.MethodPost, c.endpoint("commit", nil), body)
	if err != nil {
		return nil, err
	}
	var res api.CommitResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Fork creates a branch from an existing revision.
func (c *Client) Fork(ctx context.Context, from, branch string) (string, error) {
	body, _ := json.Marshal(map[string]string{"from": from, "branch": branch})
	data, err := c.do(ctx, http.MethodPost, c.endpoint("fork", nil), body)
	if err != nil {
		return "", err
	}
	var res struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	return res.Target, nil
}
