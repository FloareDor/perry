/**
 * Runs a fleet of agents against one repository and merges their work.
 *
 * Each agent gets its own branch, forked with a single reference write.
 * None of them clones. They search, read what they need, and commit
 * through the API, so twenty agents cost twenty branch pointers rather
 * than twenty copies of the repository.
 */

import { PerryClient, PerryError, type Change } from "./perry.ts";
import type { Agent, Task } from "./agent.ts";

export interface FleetOptions {
  baseUrl: string;
  repo: string;
  token?: string;
  base: string;
  agent: Agent;
  /** How many agents run at once. */
  concurrency: number;
  /** Fast-forward each finished branch onto the base branch. */
  merge: boolean;
  onEvent?: (e: FleetEvent) => void;
}

export type FleetEvent =
  | { type: "start"; task: string; branch: string }
  | { type: "done"; task: string; branch: string; commit: string; ms: number; summary: string }
  | { type: "failed"; task: string; branch: string; error: string }
  | { type: "merged"; task: string; branch: string; commit: string }
  | { type: "merge-skipped"; task: string; branch: string; reason: string };

export interface TaskOutcome {
  task: string;
  branch: string;
  ok: boolean;
  commit?: string;
  summary?: string;
  error?: string;
  ms: number;
  merged: boolean;
  /** Paths the agent wrote, which the merge queue replays onto the base. */
  changes?: Change[];
}

export interface FleetReport {
  outcomes: TaskOutcome[];
  succeeded: number;
  failed: number;
  merged: number;
  wallMs: number;
  bytesRead: number;
  requests: number;
}

/** Retries a commit that lost a compare-and-swap, on a fresh tip. */
const MAX_COMMIT_ATTEMPTS = 3;

export async function runFleet(tasks: Task[], opts: FleetOptions): Promise<FleetReport> {
  const client = new PerryClient({ baseUrl: opts.baseUrl, repo: opts.repo, token: opts.token });
  const emit = opts.onEvent ?? (() => {});

  const outcomes: TaskOutcome[] = new Array(tasks.length);
  const started = Date.now();

  // A shared cursor rather than fixed slices, so a slow task does not
  // leave one worker running alone while the others have finished.
  let cursor = 0;
  const workerCount = Math.max(1, Math.min(opts.concurrency, tasks.length));

  const worker = async (): Promise<void> => {
    for (;;) {
      const index = cursor++;
      if (index >= tasks.length) return;
      const task = tasks[index]!;
      const branch = `agent/${task.id}`;
      const begin = Date.now();

      emit({ type: "start", task: task.id, branch });
      try {
        await forkBranch(client, opts.base, branch);
        const result = await runOne(client, opts.agent, task, branch);
        outcomes[index] = {
          task: task.id,
          branch,
          ok: true,
          commit: result.commit,
          summary: result.summary,
          changes: result.changes,
          ms: Date.now() - begin,
          merged: false,
        };
        emit({
          type: "done",
          task: task.id,
          branch,
          commit: result.commit,
          ms: Date.now() - begin,
          summary: result.summary,
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        outcomes[index] = {
          task: task.id,
          branch,
          ok: false,
          error: message,
          ms: Date.now() - begin,
          merged: false,
        };
        emit({ type: "failed", task: task.id, branch, error: message });
      }
    }
  };

  await Promise.all(Array.from({ length: workerCount }, worker));

  // Merging runs after the fleet, one branch at a time. Agents work in
  // parallel because their branches are independent; merges are serial
  // because they all move the same reference.
  //
  // Each agent forked from the same base, so its branch is not a
  // descendant of the base once an earlier merge has landed. Advancing the
  // pointer to each branch in turn would keep only the last one and throw
  // the rest away. Instead each agent's file changes are replayed onto the
  // base as a fresh commit, which is a rebase of a known, small change set.
  let merged = 0;
  if (opts.merge) {
    const claimed = new Map<string, string>();

    for (const outcome of outcomes) {
      if (!outcome?.ok || !outcome.changes?.length) continue;

      // Two agents editing one file is a conflict this cannot resolve, so
      // the second branch is left for a human rather than guessed at.
      const collision = outcome.changes
        .map((c) => c.path)
        .find((p) => claimed.has(p));
      if (collision !== undefined) {
        const reason = `${collision} was already changed by ${claimed.get(collision)}`;
        emit({ type: "merge-skipped", task: outcome.task, branch: outcome.branch, reason });
        continue;
      }

      try {
        const tip = await client.tip(opts.base);
        const landed = await client.commit({
          branch: opts.base,
          message: `merge ${outcome.task}: ${outcome.summary ?? ""}`.trim(),
          author: `agent-${outcome.task}`,
          email: `agent-${outcome.task}@perry.local`,
          changes: outcome.changes,
          expect: tip ?? undefined,
        });
        for (const c of outcome.changes) claimed.set(c.path, outcome.task);
        outcome.merged = true;
        merged++;
        emit({ type: "merged", task: outcome.task, branch: outcome.branch, commit: landed.commit });
      } catch (err) {
        const reason = err instanceof Error ? err.message : String(err);
        emit({ type: "merge-skipped", task: outcome.task, branch: outcome.branch, reason });
      }
    }
  }

  return {
    outcomes,
    succeeded: outcomes.filter((o) => o?.ok).length,
    failed: outcomes.filter((o) => o && !o.ok).length,
    merged,
    wallMs: Date.now() - started,
    bytesRead: client.bytesRead,
    requests: client.requests,
  };
}

/** Creates the agent's branch, tolerating one left over from a prior run. */
async function forkBranch(client: PerryClient, base: string, branch: string): Promise<void> {
  try {
    await client.fork(base, branch);
  } catch (err) {
    if (err instanceof PerryError && (err.isConflict || err.status === 400)) {
      const existing = await client.tip(branch);
      if (existing) return;
    }
    throw err;
  }
}

async function runOne(
  client: PerryClient,
  agent: Agent,
  task: Task,
  branch: string,
): Promise<{ commit: string; summary: string; changes: Change[] }> {
  let lastError: unknown;

  for (let attempt = 1; attempt <= MAX_COMMIT_ATTEMPTS; attempt++) {
    const expect = await client.tip(branch);
    const result = await agent(task, client, branch);
    try {
      const commit = await client.commit({
        branch,
        message: `${task.id}: ${result.summary}`,
        author: `agent-${task.id}`,
        email: `agent-${task.id}@perry.local`,
        changes: result.changes,
        expect: expect ?? undefined,
      });
      return { commit: commit.commit, summary: result.summary, changes: result.changes };
    } catch (err) {
      lastError = err;
      // Only a lost compare-and-swap is worth repeating: the agent reruns
      // against the branch as it now stands. Any other failure will fail
      // again the same way.
      if (!(err instanceof PerryError && err.isConflict)) throw err;
    }
  }
  throw lastError;
}
