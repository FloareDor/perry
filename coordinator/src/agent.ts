/**
 * The agent step: given a task and a repository it can search but has not
 * cloned, produce a set of file changes.
 *
 * Two implementations are provided. `claudeAgent` calls the Claude Agent
 * SDK and is what the real demo runs. `scriptedAgent` performs a
 * deterministic edit with no model call, which is what the test suite and
 * the benchmark use: it keeps the measurement about perry's throughput
 * rather than about model latency, and it means the demo runs without an
 * API key.
 */

import type { Change, PerryClient } from "./perry.ts";

export interface Task {
  id: string;
  /** What the agent should do, in plain language. */
  prompt: string;
  /** A pattern the agent greps for to locate the code to change. */
  locate: string;
  /** Narrow the search to these paths. */
  paths?: string[];
}

export interface AgentResult {
  changes: Change[];
  summary: string;
  filesRead: number;
}

export type Agent = (task: Task, repo: PerryClient, branch: string) => Promise<AgentResult>;

/**
 * A deterministic agent that appends a comment to the file it finds.
 *
 * It exercises the same read path as a real agent, grep then read then
 * write, without the variance a model introduces.
 */
export const scriptedAgent: Agent = async (task, repo, branch) => {
  const found = await repo.grep(task.locate, { ref: branch, paths: task.paths, limit: 5 });
  if (found.matches.length === 0) {
    throw new Error(`task ${task.id}: nothing matched ${task.locate}`);
  }

  const target = found.matches[0]!.path;
  const current = await repo.readFile(target, branch);
  const edited = `${current.replace(/\n+$/, "")}\n\n// ${task.id}: ${task.prompt}\n`;

  return {
    changes: [{ path: target, content: edited }],
    summary: `appended a note to ${target}`,
    filesRead: 1,
  };
};

/**
 * Runs the task through the Claude Agent SDK.
 *
 * The SDK is imported lazily so the coordinator, its tests and the
 * benchmark all run without the package installed or an API key present.
 */
export const claudeAgent: Agent = async (task, repo, branch) => {
  const { query } = await import("@anthropic-ai/claude-agent-sdk");

  const found = await repo.grep(task.locate, { ref: branch, paths: task.paths, limit: 10, context: 3 });
  if (found.matches.length === 0) {
    throw new Error(`task ${task.id}: nothing matched ${task.locate}`);
  }

  // Read only the files grep pointed at. This is the whole context the
  // model gets, and it is why no clone is needed.
  const paths = [...new Set(found.matches.map((m) => m.path))].slice(0, 3);
  const files = await Promise.all(
    paths.map(async (p) => ({ path: p, content: await repo.readFile(p, branch) })),
  );

  const prompt = [
    `Task: ${task.prompt}`,
    "",
    "Here are the relevant files from the repository.",
    ...files.map((f) => `--- ${f.path} ---\n${f.content}`),
    "",
    "Reply with a JSON object only, no prose and no code fence:",
    `{"summary": "<one line>", "changes": [{"path": "<path>", "content": "<full new file contents>"}]}`,
    "Include the complete new contents of every file you change.",
  ].join("\n");

  let output = "";
  for await (const message of query({ prompt, options: { permissionMode: "bypassPermissions" } })) {
    if (message.type === "result" && "result" in message) {
      output = String(message.result);
    }
  }

  const parsed = parseAgentJson(output);
  return { changes: parsed.changes, summary: parsed.summary, filesRead: files.length };
};

/**
 * Extracts the JSON object from a model reply.
 *
 * Models wrap JSON in a code fence or add a sentence before it often
 * enough that parsing the raw string fails in normal operation, so this
 * strips a fence and falls back to the outermost braces.
 */
function parseAgentJson(raw: string): { summary: string; changes: Change[] } {
  const fence = raw.match(/```(?:json)?\s*([\s\S]*?)```/);
  const candidate = fence?.[1] ?? raw;

  const start = candidate.indexOf("{");
  const end = candidate.lastIndexOf("}");
  if (start < 0 || end <= start) {
    throw new Error(`agent returned no JSON object: ${raw.slice(0, 200)}`);
  }

  const parsed = JSON.parse(candidate.slice(start, end + 1)) as {
    summary?: string;
    changes?: Change[];
  };
  if (!parsed.changes?.length) {
    throw new Error("agent returned no changes");
  }
  return { summary: parsed.summary ?? "no summary", changes: parsed.changes };
}
