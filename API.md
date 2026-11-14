# The Perry API

Two surfaces over the same store: an HTTP API for reading and writing a repository
without a checkout, and an MCP adapter that exposes the same thing to a coding agent.

## Authentication

Git smart HTTP carries `receive-pack`, so a Perry server without a token accepts
anonymous **pushes** to any repository and creates repositories on demand. Perry
refuses to bind anything but loopback unless you set a token or pass `-insecure`.

```bash
perry serve -addr 0.0.0.0:8080 -token "$PERRY_TOKEN"
```

The token is required on every route, git and API alike, as either an
`Authorization: Bearer` header or the password in HTTP Basic auth. Git cannot send a
bearer token, so it uses Basic and the username is ignored:

```bash
git clone http://x:"$PERRY_TOKEN"@host:8080/app.git
curl -H "Authorization: Bearer $PERRY_TOKEN" http://host:8080/v1/repos/app/refs
```

`/healthz` stays open so a liveness probe needs no credential. Put the server behind
TLS, because Basic auth sends the token in clear text. One token covers every
repository and every route: no users, no per-repository scopes, no revocation.

## HTTP

| Route | What it does |
|---|---|
| `GET /v1/repos/{repo}/grep` | regex search of every file at a revision |
| `GET /v1/repos/{repo}/file` | one file at one revision, whole or by line range |
| `GET /v1/repos/{repo}/files` | paths at a revision, filtered by a glob |
| `GET /v1/repos/{repo}/tree` | one directory, with types and sizes |
| `GET /v1/repos/{repo}/refs` | refs, filtered by prefix |
| `POST /v1/repos/{repo}/fork` | a new branch, as a pointer write |
| `POST /v1/repos/{repo}/commit` | a commit, under compare-and-swap |
| `POST /v1/repos/{repo}/ref` | move a ref |

### Search

```bash
curl 'http://localhost:8080/v1/repos/app/grep?q=func+Resolve&ref=main&limit=20'
```

```json
{"matches":[{"path":"internal/api/api.go","line":41,"text":"func Resolve(ctx context.Context, ..."}],
 "truncated":false,"filesScanned":400,"filesMatched":2,"filesSkipped":3,
 "commit":"db53fe2a5c1f5d0f69bfb0ed21275201e0ab9c68"}
```

`commit` is the revision the search actually ran against, and it is what to send back
as a commit's `expect`. That pins the write to the version that was read rather than
to wherever the branch has moved since, with no extra round trip to find the tip.

`truncated` means there is more to find. `filesSkipped` counts binaries and oversized
blobs, which is a different thing: an early version derived `truncated` from the
scanned-file count, so any repository containing a binary reported every search as
truncated.

### Read

```bash
curl 'http://localhost:8080/v1/repos/app/file?ref=main&path=go.mod'
curl 'http://localhost:8080/v1/repos/app/files?ref=main&glob=internal/**/*.go'
curl 'http://localhost:8080/v1/repos/app/refs?prefix=refs/heads/agent/'
```

`file` takes `start` and `end` line numbers, and the `X-Perry-Lines` response header
says how long the whole file is. A negative `start` counts back from the end, so this
is the equivalent of `tail`:

```bash
curl -D- 'http://localhost:8080/v1/repos/app/file?ref=main&path=internal/api/api.go&start=-20'
```

Ranges exist because reading all of a large file to change one line of it is how an
agent spends its context, which costs more than the bytes do. Adding them cut
inference cost 31% and bytes on the wire 95% on the benchmark task; see
[BENCHMARKS.md](BENCHMARKS.md#chasing-the-cost-gap-three-changes-one-that-worked).

### Fork and commit

```bash
curl -X POST localhost:8080/v1/repos/app/fork -d '{"from":"main","branch":"agent/t-07"}'

curl -X POST localhost:8080/v1/repos/app/commit -d '{
  "branch":  "agent/t-07",
  "message": "t-07: add a note",
  "expect":  "3b73ec5c...",
  "changes": [{"path":"README.md","content":"<base64>"}]
}'
```

`expect` is the commit the branch must still point at, which turns the write into a
compare-and-swap. A losing `expect` returns 409 and the agent retries on the fresh
tip. Moving a ref to something that is not a fast-forward is refused unless `force` is
set.

### Edits instead of whole files

A change can carry `edits` instead of `content`. Each edit replaces an exact run of
text in the file already on the branch, so an agent changing one line of a large file
sends that line and not the file:

```bash
curl -X POST localhost:8080/v1/repos/app/commit -d '{
  "branch":  "agent/t-07",
  "message": "t-07: raise the timeout",
  "expect":  "3b73ec5c...",
  "changes": [{"path":"server.go","edits":[
    {"old":"timeout := 30 * time.Second","new":"timeout := 90 * time.Second"}
  ]}]
}'
```

Each `old` must occur exactly once or the commit is refused and names the count. An
edit that could land in two places is rejected rather than guessed at. Set `all` to
replace every occurrence on purpose. Edits apply in order, each to the result of the
last, and the file keeps its mode.

Perry does not take a unified diff. A diff carries line numbers and context that a
model reconstructs from memory, and when it gets them wrong the patch can still apply
in the wrong place. An exact string either occurs once or it does not.

The same change turned the agent benchmark around. `perry_commit` originally took each file's complete new contents, so a model editing a 733-line file
re-emitted all 733 lines: 15,040 output tokens against 585 for the arm with a normal
edit tool, and 59 extra seconds of the model typing out a file it had just been shown.

## MCP

`perry mcp` bridges one repository to any agent that speaks the
[Model Context Protocol](https://modelcontextprotocol.io). It runs on stdio, which is
how every MCP client launches a local server.

```json
{
  "mcpServers": {
    "perry": {
      "command": "perry",
      "args": ["mcp", "-server", "http://perry.internal:8080", "-repo", "app"],
      "env": { "PERRY_TOKEN": "..." }
    }
  }
}
```

For Claude Code:

```bash
claude mcp add perry --env PERRY_TOKEN=... -- perry mcp -server http://perry.internal:8080 -repo app
```

| Tool | What the agent gets |
| --- | --- |
| `perry_grep` | a regex search of every file at a revision, and the commit it searched |
| `perry_read_file` | a file's exact bytes, verbatim and unnumbered, whole or by line range |
| `perry_list_files` | the paths at a revision, filtered by a glob |
| `perry_list_tree` | one directory, with types and sizes |
| `perry_list_branches` | branches and their tips, for `expect` |
| `perry_create_branch` | a branch, as a pointer write |
| `perry_commit` | a commit, from edits or whole-file contents, under compare-and-swap |

The adapter is a client of the HTTP API, not of the object store, so the perry server
can be on another host. It does not shell out to `git`, so an agent given these tools
has no local repository to fall back on. A plain `git clone` of the branch it wrote
returns a normal repository.

## Fleet coordinator

`coordinator/` runs many agents against one perry. Each forks, greps, reads only what
matched, and commits; a serial queue then replays each branch onto `main`.

```bash
cd coordinator && npm install
node --experimental-strip-types src/index.ts --repo app --tasks tasks.json --agents 20
```

```
fleet: 20 tasks, 20 at a time, scripted agent
  t-01   38ms  9f1c0aa  appended a note to internal/api/api.go
  ...
20/20 succeeded, 20 merged, in 212ms
140 API requests, 3.1 KB read, 0 clones
```

`--mode claude` routes each task through the Claude Agent SDK instead of the scripted
edit. See [`coordinator/README.md`](coordinator/README.md).
