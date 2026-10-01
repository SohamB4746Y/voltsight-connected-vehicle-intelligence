import { describe, expect, it, vi } from "vitest";
import { ApiError, AuthClient, AuthRequired, type StoredUser } from "./auth";

const NOW = 1_800_000_000_000; // fixed clock (ms)
const valid = (t: string): StoredUser => ({ access_token: t, expires_at: NOW / 1000 + 300 });
const expired = (t: string): StoredUser => ({ access_token: t, expires_at: NOW / 1000 - 5 });
const json = (status: number, body: unknown = {}) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function harness(initial: StoredUser | null, opts: { refresh?: () => Promise<StoredUser | null>; fetchImpl?: any } = {}) {
  let user = initial;
  const refresh = vi.fn(async () => {
    const u = opts.refresh ? await opts.refresh() : valid("fresh");
    user = u;
    return u;
  });
  const login = vi.fn(async () => {});
  const fetchImpl = vi.fn(opts.fetchImpl ?? (async () => json(200, { ok: true })));
  const sleep = vi.fn(async () => {});
  const client = new AuthClient({ getUser: async () => user, refresh, login, fetchImpl: fetchImpl as any, now: () => NOW, sleep, random: () => 1 });
  return { client, refresh, login, fetchImpl, sleep, setUser: (u: StoredUser | null) => (user = u) };
}
const bearer = (call: any[]) => (call[1].headers as Record<string, string>).Authorization;

describe("A. valid token", () => {
  it("is used as is: no refresh, API succeeds", async () => {
    const h = harness(valid("tok"));
    expect(await h.client.api("/v1/me")).toEqual({ ok: true });
    expect(h.refresh).not.toHaveBeenCalled();
    expect(bearer(h.fetchImpl.mock.calls[0])).toBe("Bearer tok");
  });
});

describe("B. expired access token", () => {
  it("refreshes first, then the API succeeds with the new token", async () => {
    const h = harness(expired("old"));
    await h.client.api("/v1/me");
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(bearer(h.fetchImpl.mock.calls[0])).toBe("Bearer fresh");
  });
  it("a token inside the safety margin is refreshed before it expires", async () => {
    const h = harness({ access_token: "old", expires_at: NOW / 1000 + 30 }); // margin is 60 s
    await h.client.api("/v1/me");
    expect(h.refresh).toHaveBeenCalledTimes(1);
  });
  it("a server-side 401 (token rejected although it looked valid) renews once and retries once", async () => {
    let n = 0;
    const h = harness(valid("old"), { fetchImpl: async () => (++n === 1 ? json(401, { detail: "invalid token" }) : json(200, { ok: 1 })) });
    expect(await h.client.api("/v1/me")).toEqual({ ok: 1 });
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(h.fetchImpl).toHaveBeenCalledTimes(2);
    expect(bearer(h.fetchImpl.mock.calls[1])).toBe("Bearer fresh");
  });
});

describe("C. simultaneous requests with an expired token", () => {
  it("cause exactly one refresh", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const h = harness(expired("old"), { refresh: async () => (await gate, valid("fresh")) });
    const calls = Promise.all(Array.from({ length: 12 }, () => h.client.api("/v1/fleet/summary")));
    await Promise.resolve();
    release();
    await calls;
    expect(h.refresh).toHaveBeenCalledTimes(1);
    for (const c of h.fetchImpl.mock.calls) expect(bearer(c)).toBe("Bearer fresh");
  });
  it("concurrent 401s from the same stale token cause one renewal", async () => {
    const h = harness(valid("old"), { fetchImpl: async (_p: string, i: any) => (i.headers.Authorization === "Bearer old" ? json(401) : json(200, { ok: 1 })) });
    await Promise.all(Array.from({ length: 8 }, () => h.client.api("/v1/x")));
    expect(h.refresh).toHaveBeenCalledTimes(1);
  });
});

describe("D. refresh failure", () => {
  it("rejected by the identity provider -> login redirect, once, no data", async () => {
    const h = harness(expired("old"), { refresh: async () => { throw Object.assign(new Error("invalid_grant"), { fatal: true }); } });
    await expect(h.client.api("/v1/me")).rejects.toBeInstanceOf(AuthRequired);
    await expect(h.client.api("/v1/me")).rejects.toBeInstanceOf(AuthRequired);
    expect(h.login).toHaveBeenCalledTimes(1);
  });
  it("a network error is transient: the user is NOT logged out", async () => {
    const h = harness(expired("old"), { refresh: async () => { throw new TypeError("network"); } });
    await expect(h.client.api("/v1/me")).rejects.toMatchObject({ status: 503 });
    expect(h.login).not.toHaveBeenCalled();
  });
});

describe("F. repeated 401", () => {
  it("never loops: one renewal, one retry, then the 401 is returned", async () => {
    const h = harness(valid("old"), { fetchImpl: async () => json(401, { detail: "invalid token" }) });
    await expect(h.client.api("/v1/me")).rejects.toMatchObject({ status: 401 });
    expect(h.fetchImpl).toHaveBeenCalledTimes(2);
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(h.login).not.toHaveBeenCalled(); // a 401 after a good refresh is an authorisation answer, not a sign-out
  });
  it("a 403 does not refresh at all", async () => {
    const h = harness(valid("tok"), { fetchImpl: async () => json(403, { detail: "forbidden" }) });
    await expect(h.client.api("/v1/audit")).rejects.toBeInstanceOf(ApiError);
    expect(h.refresh).not.toHaveBeenCalled();
  });
});

function sse(chunks: string[]) {
  const enc = new TextEncoder();
  let i = 0;
  return new Response(new ReadableStream({ pull(c) { if (i < chunks.length) c.enqueue(enc.encode(chunks[i++])); else c.close(); } }), { status: 200 });
}

describe("E. live stream", () => {
  it("delivers events and reconnects with a FRESH token after the stream ends on an expired one", async () => {
    let n = 0;
    const seen: string[] = [];
    const ctl = new AbortController();
    const h = harness(valid("t1"), {
      fetchImpl: async (_p: string, i: any) => {
        seen.push(i.headers.Authorization);
        if (++n === 1) return sse(['data: {"a":1}\n\n']);
        ctl.abort();
        return sse([]);
      },
    });
    h.setUser(expired("t1")); // by the time the first stream ends the token has expired
    const got: string[] = [];
    await h.client.stream("/v1/alerts/stream", (d) => got.push(d), ctl.signal);
    expect(got).toEqual(['{"a":1}']);
    expect(seen[0]).toBe("Bearer fresh");
    expect(h.refresh.mock.calls.length).toBeGreaterThanOrEqual(1);
  });
  it("backs off exponentially (and not at all in a storm-free way) when connections keep failing", async () => {
    const ctl = new AbortController();
    let n = 0;
    const h = harness(valid("t"), { fetchImpl: async () => { if (++n >= 6) ctl.abort(); return json(503); } });
    await h.client.stream("/v1/alerts/stream", () => {}, ctl.signal);
    const delays = (h.sleep.mock.calls as unknown as number[][]).map((c) => c[0]);
    expect(delays.length).toBeGreaterThanOrEqual(4);
    for (let i = 1; i < delays.length; i++) expect(delays[i]).toBeGreaterThanOrEqual(delays[i - 1]);
    expect(Math.max(...delays)).toBeLessThanOrEqual(30_000);
  });
  it("a 401 on connect renews once; a second 401 with the renewed token sends the user to login (no storm)", async () => {
    const ctl = new AbortController();
    const h = harness(valid("t"), { fetchImpl: async () => json(401) });
    await h.client.stream("/v1/alerts/stream", () => {}, ctl.signal);
    expect(h.fetchImpl.mock.calls.length).toBeLessThanOrEqual(2);
    expect(h.login).toHaveBeenCalledTimes(1);
  });
  it("reports connecting -> live -> reconnecting", async () => {
    const ctl = new AbortController();
    const status: string[] = [];
    let n = 0;
    const h = harness(valid("t"), { fetchImpl: async () => { if (++n === 2) ctl.abort(); return sse(["data: {}\n\n"]); } });
    await h.client.stream("/v1/alerts/stream", () => {}, ctl.signal, (s) => status.push(s));
    expect(status.slice(0, 3)).toEqual(["connecting", "live", "reconnecting"]);
  });
});
