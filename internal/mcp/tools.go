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
				"Returns path:line: text for each match, and the commit it searched. " +
				"Pass that commit to perry_commit as expect; it is the version your " +
				"edits are written against, so you do not need perry_list_branches. " +
				"Use context to see the lines around a match, which is usually " +
				"enough to write an edit without reading the file at all. " +
				"include_files returns up to three matched files whole; it saves a " +
				"round trip when you genuinely need all of each one, and wastes " +
				"context when you do not.",
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
				"include_files": prop("boolean", "Return the matched files in full, up to three of them. "+
					"Use it when you intend to edit what you find."),
			}, "pattern"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Pattern       string   `json:"pattern"`
					Ref           string   `json:"ref"`
					Paths         []string `json:"paths"`
					Context       int      `json:"context"`
					Limit         int      `json:"limit"`
					CaseSensitive bool     `json:"case_sensitive"`
					IncludeFiles  bool     `json:"include_files"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Pattern == "" {
					return "", fmt.Errorf("pattern is required")
				}
				res, err := c.Grep(ctx, api.GrepOptions{
					Pattern:       a.Pattern,
					Rev:           a.Ref,
					PathPrefixes:  a.Paths,
					Context:       a.Context,
					Limit:         a.Limit,
					CaseSensitive: a.CaseSensitive,
					IncludeFiles:  a.IncludeFiles,
				})
				if err != nil {
					return "", err
				}
				return formatGrep(res), nil
			},
		},
		{
			Name: "perry_read_file",
			Description: "Read a file's exact contents at a revision. " +
				"No checkout is involved; only what you ask for crosses the network. " +
				"The text comes back verbatim with no line numbers, so it can be " +
				"quoted straight back to perry_commit as an edit's old text. " +
				"Read only the part you need: start_line and end_line take a range, " +
				"and a negative start_line counts back from the end, so -20 is the " +
				"last twenty lines. Reading a whole large file to change one line of " +
				"it fills your context with the rest.",
			InputSchema: object(map[string]any{
				"path":       prop("string", "Repository-relative path, e.g. internal/api/http.go."),
				"ref":        prop("string", "Branch, tag or commit. Defaults to HEAD."),
				"start_line": prop("integer", "First line to return, 1-based and inclusive. Negative counts back from the end: -20 is the last twenty lines. Omit for the start of the file."),
				"end_line":   prop("integer", "Last line to return, inclusive. Omit for the end of the file."),
			}, "path"),
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Path  string `json:"path"`
					Ref   string `json:"ref"`
					Start int    `json:"start_line"`
					End   int    `json:"end_line"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Path == "" {
					return "", fmt.Errorf("path is required")
				}
				data, total, err := c.ReadFileRange(ctx, a.Ref, a.Path, a.Start, a.End)
				if err != nil {
					return "", err
				}
				if !utf8.Valid(data) {
					return "", fmt.Errorf("%s is not valid UTF-8 (%d bytes); it is probably binary", a.Path, len(data))
				}
				out := string(data)
				if len(out) > maxFileChars {
					return out[:maxFileChars] +
						fmt.Sprintf("\n\n[truncated: %d of %d bytes shown. Use start_line and end_line to read a part of it.]",
							maxFileChars, len(out)), nil
				}
				// A partial read says so, with the file's real length, so
				// the model knows what it has not seen rather than
				// treating the fragment as the whole file.
				if a.Start != 0 || a.End != 0 {
					shown := strings.Count(strings.TrimSuffix(out, "\n"), "\n") + 1
					return fmt.Sprintf("[%d of %s %d lines]\n%s", shown, a.Path, total, out), nil
				}
				return out, nil
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
				"You do not need this before committing if you have already " +
				"searched: perry_grep reports the commit it searched, and that is " +
				"what to pass as expect.",
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
				"no staging area and no push. " +
				"Prefer edits: each one replaces an exact run of text in the file, " +
				"the way a normal editing tool does, so changing one line of a large " +
				"file costs one line. Send content instead only for a new file or a " +
				"deliberate rewrite of the whole thing; re-emitting a file you have " +
				"just read is slow and error-prone. " +
				"Pass expect with the commit your edits are written " +
				"against, which perry_grep reported when you found the file, so a " +
				"concurrent writer cannot be overwritten: a mismatch is rejected " +
				"rather than merged.",
			InputSchema: object(map[string]any{
				"branch":  prop("string", "Branch to commit to, e.g. agent/fix-timeout."),
				"message": prop("string", "Commit message."),
				"changes": map[string]any{
					"type":        "array",
					"description": "The files to write or delete.",
					"items": object(map[string]any{
						"path": prop("string", "Repository-relative path."),
						"edits": map[string]any{
							"type": "array",
							"description": "Replacements to make in the file that is already there. " +
								"Preferred over content for any change to an existing file. " +
								"They apply in order, each to the result of the last.",
							"items": object(map[string]any{
								"old": prop("string", "The exact text to replace, copied character for "+
									"character from perry_read_file, including indentation. "+
									"It must appear exactly once, so include enough "+
									"surrounding lines to make it unique."),
								"new": prop("string", "What replaces it. Empty deletes the matched text."),
								"all": prop("boolean", "Replace every occurrence instead of requiring a unique one."),
							}, "old", "new"),
						},
						"content": prop("string", "The file's complete new contents. For a new file or a "+
							"full rewrite; use edits to change an existing file. Omit when deleting."),
						"delete": prop("boolean", "Remove this path instead of writing it."),
						"mode":   prop("string", "File mode, e.g. 100755 for an executable. Defaults to 100644."),
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
						Path string `json:"path"`
						// Content is a pointer so an omitted field stays
						// distinguishable from an empty string: one means "this
						// change is made by edits", the other means "truncate
						// this file to nothing".
						Content *string `json:"content"`
						Delete  bool    `json:"delete"`
						Mode    string  `json:"mode"`
						Edits   []struct {
							Old string `json:"old"`
							New string `json:"new"`
							All bool   `json:"all"`
						} `json:"edits"`
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
					for _, e := range ch.Edits {
						chg.Edits = append(chg.Edits, api.Edit{Old: e.Old, New: e.New, All: e.All})
					}
					switch {
					case ch.Delete || len(chg.Edits) > 0:
						// Both leave Content nil: a deletion because perry
						// distinguishes "write an empty file" from "remove this
						// path" by nil versus empty, and an edit because the
						// server reads the current contents itself.
					case ch.Content == nil:
						return "", fmt.Errorf("%s needs content, edits, or delete", ch.Path)
					default:
						// An empty string still has to reach perry as a non-nil
						// slice, or it would read as a deletion.
						chg.Content = []byte(*ch.Content)
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
	fmt.Fprintf(&b, "%d matches in %d of %d files", len(res.Matches), res.Matched, res.Scanned)
	if res.Commit != "" {
		// Named on the first line, where a model reading top-down finds
		// it before it starts planning the edit.
		fmt.Fprintf(&b, " at commit %s", res.Commit)
	}
	b.WriteString("\n\n")
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
	for _, f := range res.Files {
		// Delimited the way a file is quoted in a conversation, because
		// that is the form the model has seen most: a header naming the
		// path, then the bytes, unaltered and unnumbered so they can be
		// quoted straight back as an edit's old text.
		fmt.Fprintf(&b, "\n\n--- %s ---\n%s", f.Path, f.Content)
	}
	if len(res.Files) > 0 {
		fmt.Fprintf(&b, "\n\n[%d file(s) returned in full; commit %s]", len(res.Files), res.Commit)
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
