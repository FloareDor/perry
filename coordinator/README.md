# Agent fleet coordinator

Runs many coding agents against one perry repository. None of them clones.

Each agent gets a branch, forked with a single reference write. It greps to
find the code to change, reads only the matching files, and commits through
perry's write API. A serial merge queue then replays each agent's changes
onto the base branch.

## Running

```bash
# in the repo root, start a server and push something into it
go build -o perry ./cmd/perry
./perry serve -data ./data

# then
cd coordinator
npm install
node --experimental-strip-types src/index.ts --repo demo --agents 20
```

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--url` | `http://localhost:8080` | perry base URL |
| `--repo` | required | repository name |
| `--tasks` | `tasks.json` | JSON array of tasks |
| `--base` | `main` | branch agents fork from |
| `--agents` | `8` | how many run at once |
| `--mode` | `scripted` | `scripted` or `claude` |
| `--no-merge` | | leave branches unmerged |
| `--json` | | emit the report as JSON |

`scripted` mode makes a deterministic edit with no model call, which keeps
benchmarks measuring perry rather than model latency and lets the demo run
without an API key. `claude` mode routes the task through the Claude Agent
SDK.

## Tasks

```json
[
  { "id": "t01", "prompt": "document the Helper function",
    "locate": "func Helper", "paths": ["src"] }
]
```

`locate` is the pattern the agent greps for. `paths` narrows the search.

## Merging

Agents run in parallel because their branches are independent. Merges are
serial because they all move one reference.

Every agent forks from the same base, so once the first merge lands the
others are no longer descendants of it. Advancing the base pointer to each
branch in turn would keep only the last and discard the rest, so the queue
replays each agent's file changes onto the base as a new commit instead.

Two agents editing the same file is a conflict the queue will not guess at.
The second branch is left in place and reported as unmerged.
