# Five minutes with Perry

A start-to-finish walkthrough: run a server, put a real repository in it, point
a coding agent at it, and watch the agent land a commit without ever cloning
anything.

Every command below was run end to end before this file was written, and the
output shown is what came back.

You need Go 1.26+ and git. The last three steps also need a logged-in
[Claude Code](https://claude.com/claude-code); the rest works without it.

Commands are POSIX shell. On Windows use Git Bash, or see
[the PowerShell notes](#powershell) at the end.

---

## 1. Build it

```bash
git clone https://github.com/FloareDor/perry
cd perry
go build -o perry ./cmd/perry
```

## 2. Start a server

Perry refuses to serve a non-loopback address without a token, so set one even
for a local experiment — the rest of the walkthrough uses it:

```bash
export PERRY_TOKEN=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
./perry serve -addr 127.0.0.1:8088 -data ./perry-data -token "$PERRY_TOKEN" &
```

```
23:51:49 perry listening on 127.0.0.1:8088, data in ./perry-data
23:51:49   git clone http://127.0.0.1:8088/demo.git
```

> The port is pinned to **8088** so every command below can be pasted as it
> stands. Perry's default is 8080; `-addr` is only here to keep the walkthrough
> consistent, and to stay out of the way of whatever is already on 8080.

The data directory is created on demand. Each repository is one SQLite file in
it; there is nothing else to set up, and no daemon but this one.

## 3. Put a repository in it

Any git repository works. Use this one, since you just cloned it:

```bash
git push http://x:"$PERRY_TOKEN"@127.0.0.1:8088/demo.git main
```

```
To http://127.0.0.1:8088/demo.git
 * [new branch]      main -> main
```

That was a stock `git push` against a server that implements the wire protocol
from scratch, with no `git` binary behind it. A repository called `demo` exists
now because the push created it.

The username in that URL is ignored. Git has no way to send a bearer token, so
perry accepts the token as a Basic auth password instead. For anything real,
put it in a credential helper rather than your shell history.

## 4. Use it without cloning

This is what a plain git remote cannot do. Search every file at a revision,
on the server:

```bash
curl -s -H "Authorization: Bearer $PERRY_TOKEN" \
  'http://127.0.0.1:8088/v1/repos/demo/grep?q=func+runMCP&ref=main'
```

```json
{"matches":[{"path":"cmd/perry/main.go","line":231,"text":"func runMCP(args []string) {"}],
 "truncated":false,"filesScanned":91,"filesMatched":1,"filesSkipped":1,
 "commit":"db53fe2a5c1f5d0f69bfb0ed21275201e0ab9c68"}
```

One request, no checkout, and it tells you which commit it searched — which is
what you pass back as `expect` when you write, so your edit lands on the
version you read. `filesSkipped` is the binaries it passed over.

Read a file, or part of one:

```bash
curl -s -H "Authorization: Bearer $PERRY_TOKEN" \
  'http://127.0.0.1:8088/v1/repos/demo/file?ref=main&path=go.mod&start=1&end=3'
```

```
module github.com/perry-dev/perry

go 1.26.0
```

Fork a branch, so an agent is not writing to `main`. This is a pointer write,
so it costs nothing and needs no working tree:

```bash
curl -s -X POST -H "Authorization: Bearer $PERRY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"from":"main","branch":"agent/tutorial"}' \
  http://127.0.0.1:8088/v1/repos/demo/fork
```

```json
{"branch":"agent/tutorial","target":"db53fe2a5c1f5d0f69bfb0ed21275201e0ab9c68"}
```

## 5. Connect a coding agent

```bash
claude mcp add perry \
  --env PERRY_TOKEN="$PERRY_TOKEN" \
  -- "$PWD/perry" mcp -server http://127.0.0.1:8088 -repo demo
```

That registers `perry mcp`, which speaks the Model Context Protocol over stdin
and stdout. It gives the agent seven tools: search, read, list files, list a
directory, list branches, create a branch, and commit.

## 6. Give it a task

Run the agent from an **empty directory**. That is the point: there is no
checkout for it to fall back on, and no repository on disk at all.

```bash
mkdir -p /tmp/empty && cd /tmp/empty

claude -p 'Find the Go function runMCP in the perry repository and add a
comment line directly above it saying: // configured by the perry tutorial
Commit to the branch agent/tutorial with the message "tutorial edit".'   --allowed-tools "mcp__perry__perry_grep,mcp__perry__perry_read_file,mcp__perry__perry_commit"   --permission-mode acceptEdits
```

```
I added the comment and committed it to `agent/tutorial`.

- Function: runMCP is in cmd/perry/main.go, line 231 before the edit.
- Change: I put `// configured by the perry tutorial` on the line directly
  above `func runMCP(args []string) {`.
- Commit: 85fcb5c on agent/tutorial, message "tutorial edit". Its parent is
  db53fe2, the version I searched.
```

Three tool calls, nothing cloned, nothing written to disk. Note the last line:
the agent pinned its write to the commit `perry_grep` told it about, so the
edit landed on the version it had actually read. That is `expect` doing its
job, and it needed no extra round trip to learn the tip.

The `--allowed-tools` list is what keeps this to three calls; without it the
agent asks before each one.

## 7. It is still a git repository

What the agent made is an ordinary git commit, so check it with git:

```bash
cd /tmp && git clone -q -b agent/tutorial \
  http://x:"$PERRY_TOKEN"@127.0.0.1:8088/demo.git check && cd check
git log --oneline -1
grep -n "perry tutorial" cmd/perry/main.go
git diff --stat HEAD~1 HEAD
git fsck
```

```
85fcb5c tutorial edit
231:// configured by the perry tutorial
 cmd/perry/main.go | 1 +
 1 file changed, 1 insertion(+)
```

A one-line diff, and `git fsck` says nothing at all, which is what it says
about a healthy repository.

Nothing about the repository is special. `git log`, `git diff`, `git fsck` and
`git push` all work, because perry writes real objects, real trees and real
packs — not an imitation of them.

## 8. Clean up

```bash
kill %1                     # the server
rm -rf perry-data /tmp/check /tmp/empty
claude mcp remove perry
```

---

## What to read next

- [API.md](API.md) — the full HTTP API and the MCP tool table.
- [BENCHMARKS.md](BENCHMARKS.md) — how this compares with an optimised git
  checkout, including the measurement Perry lost.
- `perry -h` — every flag.

## PowerShell

The shape is the same; three things differ.

```powershell
$env:PERRY_TOKEN = -join ((1..16) | ForEach-Object { '{0:x2}' -f (Get-Random -Max 256) })
Start-Process -NoNewWindow .\perry.exe "serve -addr 127.0.0.1:8088 -data .\perry-data -token $env:PERRY_TOKEN"
git push "http://x:$env:PERRY_TOKEN@127.0.0.1:8088/demo.git" main
curl.exe -s -H "Authorization: Bearer $env:PERRY_TOKEN" "http://127.0.0.1:8088/v1/repos/demo/grep?q=func+runMCP&ref=main"
```

Use `curl.exe`, not `curl` — the bare name is an alias for `Invoke-WebRequest`,
which takes entirely different arguments. Stop the server with
`Stop-Process -Name perry`.
