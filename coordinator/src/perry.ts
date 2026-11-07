/**
 * Client for perry's read and write API.
 *
 * Every method here works without a clone. That is the point: an agent
 * finds the files it needs with grep, reads only those, and commits the
 * result, so starting work costs a few kilobytes instead of a repository.
 */

export interface Match {
  path: string;
  line: number;
  text: string;
  before?: string[];
  after?: string[];
}

export interface GrepResult {
  matches: Match[];
  truncated: boolean;
  filesScanned: number;
  filesMatched: number;
}

export interface Change {
  path: string;
  content?: string;
  delete?: boolean;
  mode?: string;
}

export interface CommitResult {
  commit: string;
  tree: string;
  branch: string;
  parent?: string;
}

export interface Ref {
  name: string;
  target: string;
}

export class PerryError extends Error {
  // Declared as plain fields rather than constructor parameter properties,
  // which Node's strip-only TypeScript mode does not support.
  readonly status: number;
  readonly endpoint: string;

  constructor(message: string, status: number, endpoint: string) {
    super(message);
    this.name = "PerryError";
    this.status = status;
    this.endpoint = endpoint;
  }

  /** A losing compare-and-swap, which the caller should retry on a fresh tip. */
  get isConflict(): boolean {
    return this.status === 409;
  }
}

export interface ClientOptions {
  baseUrl: string;
  repo: string;
  token?: string;
  /** Per-request timeout. A grep on a large repository is still sub-second. */
  timeoutMs?: number;
}

export class PerryClient {
  private readonly base: string;
  private readonly repo: string;
  private readonly token?: string;
  private readonly timeoutMs: number;

  /** Bytes read through this client, for reporting against a clone. */
  bytesRead = 0;
  requests = 0;

  constructor(opts: ClientOptions) {
    this.base = opts.baseUrl.replace(/\/+$/, "");
    this.repo = opts.repo.replace(/\.git$/, "");
    this.token = opts.token;
    this.timeoutMs = opts.timeoutMs ?? 30_000;
  }

  private url(endpoint: string, params?: Record<string, string | string[] | undefined>): string {
    const u = new URL(`${this.base}/v1/repos/${this.repo}/${endpoint}`);
    for (const [k, v] of Object.entries(params ?? {})) {
      if (v === undefined) continue;
      if (Array.isArray(v)) v.forEach((item) => u.searchParams.append(k, item));
      else u.searchParams.set(k, v);
    }
    return u.toString();
  }

  private async request(endpoint: string, url: string, init?: RequestInit): Promise<Response> {
    // AbortSignal.timeout rather than a manual timer, so an aborted request
    // releases its socket instead of leaking until the body is read.
    const res = await fetch(url, {
      ...init,
      signal: AbortSignal.timeout(this.timeoutMs),
      headers: {
        ...(init?.headers ?? {}),
        ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
      },
    });
    this.requests++;
    if (!res.ok) {
      const body = await res.text().catch(() => "");
      let detail = body.slice(0, 400);
      try {
        const parsed = JSON.parse(body) as { error?: string };
        if (parsed.error) detail = parsed.error;
      } catch {
        // Not JSON; the raw body is the best message available.
      }
      throw new PerryError(`${endpoint} failed: ${detail}`, res.status, endpoint);
    }
    return res;
  }

  /** Search the repository. No checkout, no clone. */
  async grep(
    pattern: string,
    opts: { ref?: string; paths?: string[]; limit?: number; context?: number; caseSensitive?: boolean } = {},
  ): Promise<GrepResult> {
    const url = this.url("grep", {
      q: pattern,
      ref: opts.ref,
      path: opts.paths,
      limit: opts.limit?.toString(),
      context: opts.context?.toString(),
      case: opts.caseSensitive ? "true" : undefined,
    });
    const res = await this.request("grep", url);
    const text = await res.text();
    this.bytesRead += text.length;
    return JSON.parse(text) as GrepResult;
  }

  /** Read one file at one revision. */
  async readFile(path: string, ref = "HEAD"): Promise<string> {
    const res = await this.request("file", this.url("file", { ref, path }));
    const text = await res.text();
    this.bytesRead += text.length;
    return text;
  }

  /** List file paths, optionally narrowed by a glob such as `src/**\/*.go`. */
  async listFiles(glob?: string, ref = "HEAD"): Promise<string[]> {
    const res = await this.request("files", this.url("files", { ref, glob }));
    const body = (await res.json()) as { paths: string[] };
    return body.paths ?? [];
  }

  async refs(prefix = "refs/"): Promise<Ref[]> {
    const res = await this.request("refs", this.url("refs", { prefix }));
    const body = (await res.json()) as { refs: Ref[] };
    return body.refs ?? [];
  }

  /** Point a new branch at an existing revision. Copies no objects. */
  async fork(from: string, branch: string): Promise<{ branch: string; target: string }> {
    const res = await this.request("fork", this.url("fork"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ from, branch }),
    });
    return (await res.json()) as { branch: string; target: string };
  }

  /**
   * Create a commit from file contents, with no working tree.
   *
   * `expect` pins the parent the branch must still be on. Sending the tip
   * observed earlier turns the write into a compare-and-swap, so an agent
   * that raced another one is told to retry instead of silently clobbering.
   */
  async commit(req: {
    branch: string;
    message: string;
    changes: Change[];
    author?: string;
    email?: string;
    expect?: string;
  }): Promise<CommitResult> {
    const res = await this.request("commit", this.url("commit"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...req,
        changes: req.changes.map((c) => ({
          path: c.path,
          // The Go side takes []byte, which JSON-decodes from base64.
          content: c.delete ? undefined : Buffer.from(c.content ?? "", "utf8").toString("base64"),
          delete: c.delete,
          mode: c.mode,
        })),
      }),
    });
    return (await res.json()) as CommitResult;
  }

  /**
   * Move a reference, failing if it is not where the caller expected.
   *
   * Merging a finished branch is a pointer move rather than a commit, so
   * it goes through this instead of the commit endpoint.
   */
  async updateRef(name: string, target: string, expect?: string): Promise<void> {
    await this.request("ref", this.url("ref"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, target, expect: expect ?? "" }),
    });
  }

  /** Resolve a branch to its current tip, or null when it does not exist. */
  async tip(branch: string): Promise<string | null> {
    const name = branch.startsWith("refs/") ? branch : `refs/heads/${branch}`;
    const refs = await this.refs(name);
    return refs.find((r) => r.name === name)?.target ?? null;
  }
}
