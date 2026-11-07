#!/usr/bin/env node
/**
 * Command line entry point for the agent fleet.
 *
 *   fleet --url http://localhost:8080 --repo demo --tasks tasks.json --agents 20
 */

import { readFile } from "node:fs/promises";
import { runFleet, type FleetEvent } from "./fleet.ts";
import { claudeAgent, scriptedAgent, type Task } from "./agent.ts";

interface Args {
  url: string;
  repo: string;
  tasks: string;
  base: string;
  agents: number;
  token?: string;
  mode: "claude" | "scripted";
  merge: boolean;
  json: boolean;
}

const USAGE = `fleet: run coding agents against a perry repository without cloning it

Usage:
  fleet --repo <name> [flags]

Flags:
  --url <url>       perry base URL (default http://localhost:8080)
  --repo <name>     repository name, required
  --tasks <file>    JSON array of tasks (default tasks.json)
  --base <branch>   branch agents fork from (default main)
  --agents <n>      how many run at once (default 8)
  --token <token>   bearer token, if the API requires one
  --mode <mode>     claude or scripted (default scripted)
  --no-merge        leave branches unmerged
  --json            emit the report as JSON

Each task is {"id","prompt","locate","paths"?}. The agent greps for
"locate", reads only the files that match, and commits through the API.
`;

function parseArgs(argv: string[]): Args {
  const args: Args = {
    url: "http://localhost:8080",
    repo: "",
    tasks: "tasks.json",
    base: "main",
    agents: 8,
    mode: "scripted",
    merge: true,
    json: false,
  };

  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const next = (): string => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${flag} needs a value`);
      return v;
    };
    switch (flag) {
      case "--url": args.url = next(); break;
      case "--repo": args.repo = next(); break;
      case "--tasks": args.tasks = next(); break;
      case "--base": args.base = next(); break;
      case "--agents": args.agents = Number(next()); break;
      case "--token": args.token = next(); break;
      case "--mode": {
        const m = next();
        if (m !== "claude" && m !== "scripted") throw new Error(`--mode must be claude or scripted`);
        args.mode = m;
        break;
      }
      case "--no-merge": args.merge = false; break;
      case "--json": args.json = true; break;
      case "-h": case "--help":
        process.stdout.write(USAGE);
        process.exit(0);
      default:
        throw new Error(`unknown flag ${flag}`);
    }
  }

  if (!args.repo) throw new Error("--repo is required");
  if (!Number.isInteger(args.agents) || args.agents < 1) throw new Error("--agents must be a positive integer");
  return args;
}

function logEvent(e: FleetEvent): void {
  switch (e.type) {
    case "start":
      console.log(`  ${e.task.padEnd(16)} started on ${e.branch}`);
      break;
    case "done":
      console.log(`  ${e.task.padEnd(16)} ${String(e.ms).padStart(5)}ms  ${e.commit.slice(0, 8)}  ${e.summary}`);
      break;
    case "failed":
      console.log(`  ${e.task.padEnd(16)} FAILED  ${e.error}`);
      break;
    case "merged":
      console.log(`  ${e.task.padEnd(16)} merged  ${e.commit.slice(0, 8)}`);
      break;
    case "merge-skipped":
      console.log(`  ${e.task.padEnd(16)} not merged: ${e.reason}`);
      break;
  }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  const tasks = JSON.parse(await readFile(args.tasks, "utf8")) as Task[];
  if (!Array.isArray(tasks) || tasks.length === 0) {
    throw new Error(`${args.tasks} holds no tasks`);
  }

  if (!args.json) {
    console.log(`fleet: ${tasks.length} tasks, ${args.agents} at a time, ${args.mode} agent`);
    console.log(`       ${args.url}/${args.repo}, forking from ${args.base}\n`);
  }

  const report = await runFleet(tasks, {
    baseUrl: args.url,
    repo: args.repo,
    token: args.token,
    base: args.base,
    agent: args.mode === "claude" ? claudeAgent : scriptedAgent,
    concurrency: args.agents,
    merge: args.merge,
    onEvent: args.json ? undefined : logEvent,
  });

  if (args.json) {
    process.stdout.write(JSON.stringify(report, null, 2) + "\n");
  } else {
    const kb = (report.bytesRead / 1024).toFixed(1);
    console.log(`\n${report.succeeded}/${tasks.length} succeeded, ${report.merged} merged, in ${report.wallMs}ms`);
    console.log(`${report.requests} API requests, ${kb} KB read, 0 clones`);
  }

  process.exit(report.failed > 0 ? 1 : 0);
}

main().catch((err: unknown) => {
  console.error(`fleet: ${err instanceof Error ? err.message : String(err)}`);
  process.exit(2);
});
