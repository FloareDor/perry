package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/perry-dev/perry/internal/api"
)

// This file answers the one question every earlier benchmark in this package
// dodged: does a real coding agent, with a model in the loop, finish a real
// task faster against perry than against an optimised git checkout?
//
// Everything else here measures perry against git. This measures an agent
// against an agent. The difference matters, because an agent's wall clock is
// dominated by inference, not by I/O, and a transport that is ten times
// faster can still be invisible in the total. A benchmark that cannot show
// that is not worth running.
//
// The run is gated twice over, because unlike the rest of the package it
// spends real money and needs a logged-in Claude Code, so it cannot run in CI
// and must not run by accident.

// agentModel is pinned so both arms face the same model. Leaving it to the
// default would let a change in the CLI's default silently reprice the
// comparison, and would let the two arms diverge if they ran on different
// days.
const agentModel = "claude-sonnet-5-5"

// agentTimeout bounds one task. A model that has not finished a three-step
// edit in five minutes is stuck, and recording it as stuck is more useful
// than waiting.
const agentTimeout = 5 * time.Minute

// requireAgentBench gates the real-agent comparison.
func requireAgentBench(t testing.TB) string {
	t.Helper()
	requireBench(t)
	if os.Getenv("PERRY_AGENT_BENCH") == "" {
		t.Skip("the real-agent comparison is opt in, and costs money. Run it with:\n" +
			"\tPERRY_BENCH=1 PERRY_AGENT_BENCH=1 go test ./internal/bench -run RealAgent -v -timeout 180m")
	}
	bin, err := findClaude()
	if err != nil {
		t.Skipf("%v", err)
	}
	t.Logf("driving %s", bin)
	return bin
}

// findClaude locates a real Claude Code CLI and proves it is one.
//
// Plain exec.LookPath is not enough on Windows. PATHEXT makes Go prefer a
// .cmd shim over an .exe further along PATH, and the first run of this
// harness picked up an unrelated claude.cmd wrapping a Python script, which
// exited asking for ANTHROPIC_API_KEY while the real CLI sat two directories
// later authenticated and ready. The shell had been resolving the .exe all
// along, so the two disagreed about what "claude" meant.
//
// Each candidate is therefore validated by asking it for its version, rather
// than trusted because of its name.
func findClaude() (string, error) {
	var candidates []string
	if override := os.Getenv("PERRY_CLAUDE_BIN"); override != "" {
		candidates = append(candidates, override)
	}

	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		// An .exe before the bare name, and no .cmd or .bat: a shim that
		// re-launches something else is exactly what went wrong before.
		names = []string{"claude.exe", "claude"}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, name := range names {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				candidates = append(candidates, p)
			}
		}
	}

	var tried []string
	for _, c := range candidates {
		cmd := exec.Command(c, "--version")
		cmd.Stdin = nil
		out, err := cmd.Output()
		if err == nil && strings.Contains(string(out), "Claude Code") {
			return c, nil
		}
		tried = append(tried, filepath.Base(c))
	}
	return "", fmt.Errorf("no working Claude Code CLI found (tried: %s). "+
		"The real-agent comparison needs a logged-in Claude Code; "+
		"point PERRY_CLAUDE_BIN at it if it is not on PATH",
		strings.Join(tried, ", "))
}

// agentRun is one task attempt by one arm.
type agentRun struct {
	arm   string
	rtt   time.Duration
	trial int

	setup time.Duration // clone, or nothing
	agent time.Duration // what the CLI reports for itself
	total time.Duration // setup plus agent, which is what a developer waits

	turns     int
	inTok     int
	outTok    int
	cacheRead int
	costUSD   float64

	wire int64 // bytes over the shaped socket, request plus response
	reqs int64
	disk int64 // what the arm left on disk

	landed bool   // the edit is on the server, read back from the server
	denied int    // tools the arm reached for and was refused
	note   string // why it failed, when it did
}

// claudeResult is the part of `claude --output-format json` this harness
// reads. The CLI reports plenty more; pinning the fields keeps a change in
// the rest from breaking the parse.
type claudeResult struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	DurationMS int64   `json:"duration_ms"`
	Result     string  `json:"result"`
	CostUSD    float64 `json:"total_cost_usd"`
	Usage      struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	PermissionDenials []any `json:"permission_denials"`
}

// runClaude invokes the CLI headlessly and parses its single JSON result.
//
// Stdin is closed rather than inherited: the CLI waits a few seconds for
// piped input otherwise, which would land in every measurement.
func runClaude(t *testing.T, bin, cwd, prompt string, extra []string, logTo string) (*claudeResult, time.Duration, error) {
	t.Helper()

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", agentModel,
		// Without this the harness would inherit whatever MCP servers the
		// developer running it happens to have configured, which could hand
		// either arm tools the comparison is supposed to withhold.
		"--strict-mcp-config",
	}
	args = append(args, extra...)

	ctx, cancel := context.WithTimeout(context.Background(), agentTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	cmd.Stdin = nil
	// The environment is inherited unchanged. An earlier version set
	// CLAUDE_CODE_ENTRYPOINT to mark these as benchmark runs, and every cell
	// failed in 150ms: the CLI reads that variable to choose between the
	// logged-in credentials and an API key, so an unrecognised value sent it
	// down the API-key path and it exited asking for ANTHROPIC_API_KEY.

	start := time.Now()
	out, err := cmd.Output()
	elapsed := time.Since(start)

	if logTo != "" {
		_ = os.WriteFile(logTo, out, 0o644)
	}
	if ctx.Err() != nil {
		return nil, elapsed, fmt.Errorf("timed out after %s", agentTimeout)
	}
	if err != nil {
		// The CLI reports a startup failure on stdout, not stderr, so both
		// are needed here. Reporting only stderr made the first run of this
		// harness look like a bare "exit status 1" when the CLI had in fact
		// printed exactly what was wrong.
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		detail := firstLine(strings.TrimSpace(string(out)))
		if detail == "" {
			detail = firstLine(stderr)
		}
		return nil, elapsed, fmt.Errorf("claude: %v: %s", err, detail)
	}

	var res claudeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, elapsed, fmt.Errorf("claude returned unparseable JSON: %v", err)
	}
	return &res, elapsed, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestRealAgentComparison runs the same task through a real model twice over:
// once with only perry's MCP tools, once with a shell and an optimised git
// checkout.
func TestRealAgentComparison(t *testing.T) {
	bin := requireAgentBench(t)

	// One corpus. grpc-go is the larger of the two and the one where setup
	// cost is visible; running both would double a bill without changing the
	// shape of the answer.
	spec := realRepos[1]
	c := buildRealCorpus(t, spec)

	// Build the binary the MCP adapter runs as. The agent launches it, so it
	// has to exist on disk rather than in the test process.
	perryBin := filepath.Join(t.TempDir(), "perry"+exeSuffix())
	build := exec.Command("go", "build", "-o", perryBin, "github.com/perry-dev/perry/cmd/perry")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building perry: %v\n%s", err, out)
	}

	results := filepath.Join(repoRoot(t), "testdata", "agent-runs")
	if err := os.MkdirAll(results, 0o755); err != nil {
		t.Fatal(err)
	}

	trials := envInt("PERRY_AGENT_TRIALS", 3)
	rtts := envRTTs("PERRY_AGENT_RTTS", []time.Duration{0, 75 * time.Millisecond})

	gitRoot := t.TempDir()
	gitSrv := gitHTTPBackend(t, gitRoot)

	var runs []agentRun
	for _, rtt := range rtts {
		gitNet := newNetShaper(t, gitSrv.URL, rtt)
		perryNet := newNetShaper(t, c.ts.URL, rtt)

		for trial := 1; trial <= trials; trial++ {
			// A marker unique to this cell, so "did the edit land" is
			// answered by reading the published file back off the server
			// rather than by trusting the model's own report of success.
			marker := fmt.Sprintf("// perry-bench %d-%d", rtt.Milliseconds(), time.Now().UnixNano())

			runs = append(runs,
				perryArm(t, bin, c, perryBin, perryNet, rtt, trial, marker, results),
				gitArm(t, bin, c, gitRoot, gitNet, rtt, trial, marker, results,
					"git shallow", "shallow", false,
					"--depth", "1"),
				gitArm(t, bin, c, gitRoot, gitNet, rtt, trial, marker, results,
					"git sparse (told the dir)", "sparse", true,
					"--depth", "1", "--filter=blob:none", "--sparse"),
			)
		}
	}

	reportAgentRuns(t, spec, c, runs, trials)

	// A benchmark that measured nothing must not report success. The first
	// run of this harness printed a full table of zeroes and passed, which is
	// a worse outcome than a red test.
	landed := 0
	for _, r := range runs {
		if r.landed {
			landed++
		}
	}
	if landed == 0 {
		t.Fatalf("no arm completed the task in any of %d runs; the harness measured nothing", len(runs))
	}
}

// perryArm gives the model perry's MCP tools and nothing else: no shell, no
// filesystem, no git. It is run from an empty directory, so there is nothing
// local to fall back on even if it tried.
func perryArm(t *testing.T, bin string, c *corpus, perryBin string, net *netShaper,
	rtt time.Duration, trial int, marker, results string) agentRun {

	run := agentRun{arm: "perry (MCP)", rtt: rtt, trial: trial}
	branch := fmt.Sprintf("agent/perry-%d-%d", rtt.Milliseconds(), trial)

	// The branch is created off the clock, to match the git arms: each of
	// those gets its own fork of the origin before its timer starts, so
	// charging perry for a branch creation the others do not pay for would
	// tilt the comparison.
	ctx := context.Background()
	if _, err := api.Fork(ctx, c.store, "main", branch); err != nil {
		t.Fatalf("preparing %s: %v", branch, err)
	}

	work := t.TempDir() // deliberately empty
	cfgPath := filepath.Join(work, "mcp.json")
	cfg := map[string]any{"mcpServers": map[string]any{
		"perry": map[string]any{
			"command": perryBin,
			"args":    []string{"mcp", "-server", net.srv.URL, "-repo", "bench"},
		},
	}}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := fmt.Sprintf(`You are working on a Go repository that is hosted on a perry server.

There is no local checkout and you cannot create one: you have the perry_* tools and nothing else. Do not try to clone anything.

Task:
1. Find the file that contains the string %q.
2. Read that file.
3. Append this exact line to the very end of it, after the last line: %s
4. Commit the result to the branch %q with the message "agent edit".

Keep the rest of the file byte-for-byte unchanged. When you are done, report the path you changed and the commit id.`,
		c.spec.needle, marker, branch)

	net.reset()
	start := time.Now()
	res, elapsed, err := runClaude(t, bin, work, prompt, []string{
		"--mcp-config", cfgPath,
		"--allowed-tools", strings.Join([]string{
			"mcp__perry__perry_grep",
			"mcp__perry__perry_read_file",
			"mcp__perry__perry_list_files",
			"mcp__perry__perry_list_tree",
			"mcp__perry__perry_list_branches",
			"mcp__perry__perry_create_branch",
			"mcp__perry__perry_commit",
		}, ","),
	}, filepath.Join(results, fmt.Sprintf("perry-rtt%d-t%d.json", rtt.Milliseconds(), trial)))

	run.total = time.Since(start)
	run.setup = 0 // there is nothing to set up, which is the claim
	run.wire, run.reqs = net.wire(), net.reqs.Load()
	run.disk = 0

	if err != nil {
		run.note = err.Error()
		run.agent = elapsed
		return run
	}
	fillFromClaude(&run, res)

	// Verified against the server, by a path the agent did not write through.
	published, rerr := api.ReadFile(ctx, c.store, branch, findMarkedPath(t, c, branch, c.spec.needle))
	if rerr != nil {
		run.note = "reading the published file: " + rerr.Error()
		return run
	}
	run.landed = strings.Contains(string(published), strings.TrimSpace(marker))
	if !run.landed && run.note == "" {
		run.note = "the marker is not in the published file"
	}
	return run
}

// gitArm gives the model a shell and a real checkout, cloned the best way the
// earlier benchmarks found. The clone is on the clock, because a developer
// waiting for an agent waits for it too.
func gitArm(t *testing.T, bin string, c *corpus, gitRoot string, net *netShaper,
	rtt time.Duration, trial int, marker, results string,
	arm, variant string, sparse bool, cloneArgs ...string) agentRun {

	run := agentRun{arm: arm, rtt: rtt, trial: trial}

	name := fmt.Sprintf("%s-%d-%d.git", variant, rtt.Milliseconds(), trial)
	origin := forkOrigin(t, c.origin, gitRoot, name)
	remote := net.srv.URL + "/" + name
	parent := t.TempDir()
	work := filepath.Join(parent, "work")

	net.reset()
	start := time.Now()

	args := append([]string{"clone", "-q"}, cloneArgs...)
	args = append(args, remote, work)
	gitB(t, parent, args...)
	if sparse {
		gitB(t, work, "sparse-checkout", "set", c.spec.sparse)
	}
	// Committing needs an identity, and the harness must not depend on the
	// developer's global git config.
	gitB(t, work, "config", "user.email", "agent@perry.local")
	gitB(t, work, "config", "user.name", "perry-agent")
	run.setup = time.Since(start)

	where := "somewhere in this repository"
	if sparse {
		// This arm is told which directory to look in. That is git's best
		// case and not a realistic one, and the table says so.
		where = fmt.Sprintf("in the %s/ directory (this is a sparse checkout and only that directory is present)", c.spec.sparse)
	}

	prompt := fmt.Sprintf(`You are in a git working tree. The repository is already cloned at the current directory and a remote called "origin" is configured.

Task:
1. Find the file that contains the string %q, %s.
2. Append this exact line to the very end of that file, after the last line: %s
3. Commit the change with the message "agent edit" and push it to origin on the branch main.

Keep the rest of the file byte-for-byte unchanged. When you are done, report the path you changed and the commit id.`,
		c.spec.needle, where, marker)

	res, elapsed, err := runClaude(t, bin, work, prompt, []string{
		"--allowed-tools", "Bash,Read,Edit,Write,Glob,Grep",
		"--permission-mode", "acceptEdits",
	}, filepath.Join(results, fmt.Sprintf("%s-rtt%d-t%d.json", variant, rtt.Milliseconds(), trial)))

	run.total = time.Since(start)
	run.wire, run.reqs = net.wire(), net.reqs.Load()
	run.disk = treeBytes(work)

	if err != nil {
		run.note = err.Error()
		run.agent = elapsed
		return run
	}
	fillFromClaude(&run, res)

	// Verified on the origin, not in the working tree: an agent that edited
	// and committed but never pushed has not finished the task.
	out, gerr := gitQuiet(origin, "grep", "-l", strings.TrimSpace(marker), "main")
	if gerr != nil {
		run.note = "the marker is not on origin/main"
		return run
	}
	run.landed = strings.TrimSpace(out) != ""
	if !run.landed && run.note == "" {
		run.note = "the marker is not on origin/main"
	}
	return run
}

func fillFromClaude(run *agentRun, res *claudeResult) {
	run.agent = time.Duration(res.DurationMS) * time.Millisecond
	run.turns = res.NumTurns
	run.inTok = res.Usage.InputTokens + res.Usage.CacheCreationTokens
	run.outTok = res.Usage.OutputTokens
	run.cacheRead = res.Usage.CacheReadTokens
	run.costUSD = res.CostUSD
	run.denied = len(res.PermissionDenials)
	if res.IsError {
		run.note = "the model reported failure: " + firstLine(res.Result)
	}
}

// findMarkedPath locates the file the perry arm was supposed to edit, so the
// verification reads the same path the task named rather than trusting the
// model to report it.
func findMarkedPath(t *testing.T, c *corpus, branch, needle string) string {
	t.Helper()
	res, err := api.Grep(context.Background(), c.store, api.GrepOptions{
		Pattern: needle, Rev: branch, Limit: 5,
	})
	if err != nil || len(res.Matches) == 0 {
		t.Fatalf("the needle %q is not in %s: %v", needle, branch, err)
	}
	return res.Matches[0].Path
}

// gitQuiet runs git and returns its output, without failing the test. Used
// where a non-zero exit is an answer rather than an error, such as a grep
// that found nothing.
func gitQuiet(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnvB()
	out, err := cmd.Output()
	return string(out), err
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func repoRoot(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// envRTTs lets a smoke run narrow the matrix without editing the test. The
// full run is two latencies; checking that the plumbing works needs one.
func envRTTs(name string, def []time.Duration) []time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var out []time.Duration
	for _, part := range strings.Split(v, ",") {
		var ms int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &ms); err != nil || ms < 0 {
			continue
		}
		out = append(out, time.Duration(ms)*time.Millisecond)
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}

// reportAgentRuns prints the per-run detail and a median summary.
//
// Medians rather than means, and every run printed: an LLM's wall clock has a
// long right tail, so one slow trial moves a mean more than it should, and a
// summary that hides the spread would overstate how repeatable any of this
// is.
func reportAgentRuns(t *testing.T, spec repoSpec, c *corpus, runs []agentRun, trials int) {
	t.Helper()

	t.Logf("\nreal agent, model in the loop: %s, %s, %d commits, %d files, pack %s",
		agentModel, spec.label, c.commits, c.files, humanBytes(c.packed))
	t.Logf("%d trials per cell. Every run is listed, because an LLM's wall clock is not repeatable.\n", trials)

	t.Logf("%-26s %5s %3s  %8s %8s %8s %5s %7s %8s %9s %6s %s",
		"arm", "rtt", "#", "setup", "agent", "total", "turns", "out tok", "cost", "wire", "reqs", "landed")
	for _, r := range runs {
		landed := "no"
		if r.landed {
			landed = "yes"
		}
		if r.note != "" {
			landed += " (" + firstLine(r.note) + ")"
		}
		t.Logf("%-26s %4dms %3d  %7dms %7dms %7dms %5d %7d %8.4f %9s %6d %s",
			r.arm, r.rtt.Milliseconds(), r.trial,
			r.setup.Milliseconds(), r.agent.Milliseconds(), r.total.Milliseconds(),
			r.turns, r.outTok, r.costUSD, humanBytes(r.wire), r.reqs, landed)
	}

	// Medians per arm per rtt, over the runs that actually finished the task.
	type key struct {
		arm string
		rtt time.Duration
	}
	groups := map[key][]agentRun{}
	var order []key
	for _, r := range runs {
		k := key{r.arm, r.rtt}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}

	t.Logf("\nmedian of the runs that landed the edit:")
	t.Logf("%-26s %5s %5s  %8s %8s %8s %5s %9s %8s",
		"arm", "rtt", "ok", "setup", "agent", "total", "turns", "wire", "cost")
	for _, k := range order {
		g := groups[k]
		var ok []agentRun
		for _, r := range g {
			if r.landed {
				ok = append(ok, r)
			}
		}
		if len(ok) == 0 {
			t.Logf("%-26s %4dms %d/%d  %s", k.arm, k.rtt.Milliseconds(), 0, len(g), "nothing landed")
			continue
		}
		t.Logf("%-26s %4dms %d/%d  %7dms %7dms %7dms %5d %9s %8.4f",
			k.arm, k.rtt.Milliseconds(), len(ok), len(g),
			medianDur(ok, func(r agentRun) time.Duration { return r.setup }).Milliseconds(),
			medianDur(ok, func(r agentRun) time.Duration { return r.agent }).Milliseconds(),
			medianDur(ok, func(r agentRun) time.Duration { return r.total }).Milliseconds(),
			medianInt(ok, func(r agentRun) int { return r.turns }),
			humanBytes(medianInt64(ok, func(r agentRun) int64 { return r.wire })),
			medianFloat(ok, func(r agentRun) float64 { return r.costUSD }))
	}

	// A run that reached for a tool it was not given says the arm's tool set
	// did not fit the task, which is a finding about the adapter, not noise.
	for _, r := range runs {
		if r.denied > 0 {
			t.Logf("note: %s rtt%dms trial %d was refused %d tool call(s) it tried to make",
				r.arm, r.rtt.Milliseconds(), r.trial, r.denied)
		}
	}
}

func medianDur(rs []agentRun, f func(agentRun) time.Duration) time.Duration {
	v := make([]time.Duration, len(rs))
	for i, r := range rs {
		v[i] = f(r)
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
}

func medianInt(rs []agentRun, f func(agentRun) int) int {
	v := make([]int, len(rs))
	for i, r := range rs {
		v[i] = f(r)
	}
	sort.Ints(v)
	return v[len(v)/2]
}

func medianInt64(rs []agentRun, f func(agentRun) int64) int64 {
	v := make([]int64, len(rs))
	for i, r := range rs {
		v[i] = f(r)
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
}

func medianFloat(rs []agentRun, f func(agentRun) float64) float64 {
	v := make([]float64, len(rs))
	for i, r := range rs {
		v[i] = f(r)
	}
	sort.Float64s(v)
	return v[len(v)/2]
}
