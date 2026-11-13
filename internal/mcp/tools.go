package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/perry-dev/perry/internal/api"
)

// maxFileChars caps what one read returns to the model. A multi-megabyte
// generated file would otherwise blow the context window on a single call,
// and a truncated read the model is told about is more useful than a reply
// it cannot fit.
const maxFileChars = 400_000

// Tools builds the tool set for one repository.
//
// The descriptions are written for a model, not for a manual. Each says what
// the tool does, and more importantly when to reach for it rather than the
// shell: an agent that has git on its PATH will default to cloning unless
// the tool text tells it there is nothing to clone.
func Tools(c *Client) []Tool {
	return []Tool{
		{
			Name: "perry_grep",
			Description: "Search the whole repository for a Go regular expression. " +
				"Use this instead of cloning the repository or running grep locally: " +
				"the search runs on the server against every file at a revision, so " +
				"there is no checkout and nothing is downloaded but the matches. " +
				"Returns path:line: text for each match.",
			InputSchema: object(map[string]any{
				"pattern": prop("string", "Go regular expression to search for."),
				"ref":     prop("string", "Branch, tag or commit to search. Defaults to HEAD."),
				"paths": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Restrict the search to these directory or file prefixes.",
				},
				"context":        prop("integer", "Lines of context to include on each side of a match."),
				"limit":          prop("integer", "Stop after this many matches. Defaults to 1000."),
				"case_sensitive": prop("boolean", "Match case exactly. Defaults to false."),
			}, "pattern"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Pattern       string   `json:"pattern"`
					Ref           string   `json:"ref"`
					Paths         []string `json:"paths"`
					Context       int      `json:"context"`
					Limit         int      `json:"limit"`
					CaseSensitive bool     `json:"case_sensitive"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Pattern == "" {
					return "", fmt.Errorf("pattern is required")
				}
				res, err := c.Grep(ctx, a.Pattern, a.Ref, a.Paths, a.Context, a.Limit, a.CaseSensitive)
				if err != nil {
					return "", err
				}
				return formatGrep(res), nil
			},
		},
		{
			Name: "perry_read_file",
			Description: "Read one file's exact contents at a revision. " +
				"No checkout is involved; only this file crosses the network. " +
				"The text comes back verbatim with no line numbers, so it can be " +
				"edited and handed straight back to perry_commit.",
			InputSchema: object(map[string]any{
				"path": prop("string", "Repository-relative path, e.g. internal/api/http.go."),
				"ref":  prop("string", "Branch, tag or commit. Defaults to HEAD."),
			}, "path"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Path string `json:"path"`
					Ref  string `json:"ref"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Path == "" {
					return "", fmt.Errorf("path is required")
				}
				data, err := c.ReadFile(ctx, a.Ref, a.Path)
				if err != nil {
					return "", err
				}
				if !utf8.Valid(data) {
					return "", fmt.Errorf("%s is not valid UTF-8 (%d bytes); it is probably binary", a.Path, len(data))
				}
				if len(data) > maxFileChars {
					return string(data[:maxFileChars]) +
						fmt.Sprintf("\n\n[truncated: %d of %d bytes shown]", maxFileChars, len(data)), nil
				}
				return string(data), nil
			},
		},
		{
			Name: "perry_list_files",
			Description: "List the repository's file paths at a revision, optionally " +
				"filtered by a glob such as *.go or internal/**. Use this to see " +
				"the layout before reading anything.",
			InputSchema: object(map[string]any{
				"glob": prop("string", "Glob to filter paths, e.g. *_test.go. Empty lists everything."),
				"ref":  prop("string", "Branch, tag or commit. Defaults to HEAD."),
			}),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Glob string `json:"glob"`
					Ref  string `json:"ref"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				paths, err := c.ListFiles(ctx, a.Ref, a.Glob)
				if err != nil {
					return "", err
				}
				if len(paths) == 0 {
					return "no files matched", nil
				}
				return fmt.Sprintf("%d files\n\n%s", len(paths), strings.Join(paths, "\n")), nil
			},
		},
		{
			Name:        "perry_list_tree",
			Description: "List the entries directly under one directory, with type and size.",
			InputSchema: object(map[string]any{
				"path": prop("string", "Directory to list. Empty lists the repository root."),
				"ref":  prop("string", "Branch, tag or commit. Defaults to HEAD."),
			}),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Path string `json:"path"`
					Ref  string `json:"ref"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				entries, err := c.ListTree(ctx, a.Ref, a.Path)
				if err != nil {
					return "", err
				}
				var b strings.Builder
				for _, e := range entries {
					if e.Type == "tree" {
						fmt.Fprintf(&b, "%s/\n", e.Path)
						continue
					}
					fmt.Fprintf(&b, "%s\t%d bytes\n", e.Path, e.Size)
				}
				if b.Len() == 0 {
					return "empty directory", nil
				}
				return b.String(), nil
			},
		},
		{
			Name: "perry_list_branches",
			Description: "List the repository's branches and the commit each points at. " +
				"Call this before committing, to learn the current tip to pass as " +
				"expect.",
			InputSchema: object(map[string]any{
				"prefix": prop("string", "Reference prefix to filter by, e.g. refs/heads/."),
			}),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Prefix string `json:"prefix"`
				}
				_ = json.Unmarshal(raw, &a)
				refs, err := c.Refs(ctx, a.Prefix)
				if err != nil {
					return "", err
				}
				if len(refs) == 0 {
					return "no references", nil
				}
				var b strings.Builder
				for _, r := range refs {
					fmt.Fprintf(&b, "%s\t%s\n", r["target"], r["name"])
				}
				return b.String(), nil
			},
		},
		{
			Name: "perry_create_branch",
			Description: "Create a branch from an existing revision. This is a pointer " +
				"write on the server, so it costs nothing and needs no working tree. " +
				"Do this before making changes, rather than committing to a shared branch.",
			InputSchema: object(map[string]any{
				"branch": prop("string", "Name of the new branch, e.g. agent/fix-timeout."),
				"from":   prop("string", "Revision to branch from. Defaults to HEAD."),
			}, "branch"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Branch string `json:"branch"`
					From   string `json:"from"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Branch == "" {
					return "", fmt.Errorf("branch is required")
				}
				target, err := c.Fork(ctx, a.From, a.Branch)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("created %s at %s", a.Branch, target), nil
			},
		},
		{
			Name: "perry_commit",
			Description: "Commit edited files straight to a branch, with no working tree, " +
				"no staging area and no push. Each change carries the file's complete " +
				"new contents, not a diff. Pass expect with the commit the branch " +
				"currently points at (from perry_list_branches) so a concurrent writer " +
				"cannot be overwritten: a mismatch is rejected rather than merged.",
			InputSchema: object(map[string]any{
				"branch":  prop("string", "Branch to commit to, e.g. agent/fix-timeout."),
				"message": prop("string", "Commit message."),
				"changes": map[string]any{
					"type":        "array",
					"description": "The files to write or delete.",
					"items": object(map[string]any{
						"path":    prop("string", "Repository-relative path."),
						"content": prop("string", "The file's complete new contents. Omit when deleting."),
						"delete":  prop("boolean", "Remove this path instead of writing it."),
						"mode":    prop("string", "File mode, e.g. 100755 for an executable. Defaults to 100644."),
					}, "path"),
				},
				"author": prop("string", "Author name. Defaults to the adapter's -author flag."),
				"email":  prop("string", "Author email."),
				"expect": prop("string", "The commit the branch must currently point at."),
			}, "branch", "message", "changes"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Branch  string `json:"branch"`
					Message string `json:"message"`
					Author  string `json:"author"`
					Email   string `json:"email"`
					Expect  string `json:"expect"`
					Changes []struct {
						Path    string `json:"path"`
						Content string `json:"content"`
						Delete  bool   `json:"delete"`
						Mode    string `json:"mode"`
					} `json:"changes"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Branch == "" || a.Message == "" {
					return "", fmt.Errorf("branch and message are required")
				}
				if len(a.Changes) == 0 {
					return "", fmt.Errorf("changes must list at least one file")
				}

				req := api.CommitRequest{
					Branch:  a.Branch,
					Message: a.Message,
					Author:  a.Author,
					Email:   a.Email,
					Expect:  a.Expect,
				}
				for _, ch := range a.Changes {
					if ch.Path == "" {
						return "", fmt.Errorf("every change needs a path")
					}
					chg := api.Change{Path: ch.Path, Delete: ch.Delete, Mode: ch.Mode}
					if !ch.Delete {
						// A deletion leaves Content nil on purpose: perry
						// distinguishes "write an empty file" from "remove
						// this path" by nil versus empty, so an empty string
						// here must still produce a non-nil slice.
						chg.Content = []byte(ch.Content)
						if chg.Content == nil {
							chg.Content = []byte{}
						}
					}
					req.Changes = append(req.Changes, chg)
				}

				res, err := c.Commit(ctx, req)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("committed %s to %s (tree %s, parent %s)",
					res.Commit, res.Branch, res.Tree, orNone(res.Parent)), nil
			},
		},
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// formatGrep renders matches the way a developer reads them, because that is
// also the form a model has seen most of in its training data: one match per
// line, prefixed with path and line number.
func formatGrep(res *api.GrepResult) string {
	if len(res.Matches) == 0 {
		return fmt.Sprintf("no matches (%d files scanned)", res.Scanned)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches in %d of %d files\n\n", len(res.Matches), res.Matched, res.Scanned)
	for _, m := range res.Matches {
		for _, l := range m.Before {
			fmt.Fprintf(&b, "%s-%s\n", m.Path, l)
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
		for _, l := range m.After {
			fmt.Fprintf(&b, "%s-%s\n", m.Path, l)
		}
	}
	if res.Truncated {
		b.WriteString("\n[truncated: raise limit or narrow paths for more]")
	}
	return b.String()
}

// object builds a JSON Schema object node. Hand-writing these inline makes
// the tool table unreadable, and a schema struct would be more machinery
// than seven tools justify.
func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}
