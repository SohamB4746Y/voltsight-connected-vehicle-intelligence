// Token lifecycle for the SPA, independent of any browser or OIDC library so it can be unit-tested.
//
// Why this exists: the access token lives a few minutes (realm accessTokenLifespan), while the Keycloak SSO session
// lives much longer. The first version stored the token once at start-up, so after expiry every API call and the
// live stream answered 401 "invalid token", and a page reload dropped to the Sign-in screen. The rules here:
//   * every request gets a token that is valid for at least `marginSec` more seconds; if not, it is refreshed first
//   * refreshes are single-flight: any number of concurrent requests cause one refresh-token grant
//   * a 401 triggers ONE coordinated renewal and ONE retry of that request; a second 401 is returned to the caller
//   * only a refresh the identity provider REJECTS (the SSO session is gone) sends the user to the login page;
//     a network error is transient and never logs anyone out
//   * the live stream reconnects with exponential backoff and a fresh token, and never reconnects with an
//     expired one
export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

/** Thrown when the user must sign in again (the redirect has been started). */
export class AuthRequired extends Error {
  constructor() {
    super("sign-in required");
  }
}

export interface StoredUser {
  access_token: string;
  /** unix seconds */
  expires_at?: number;
}

export interface AuthDeps {
  getUser(): Promise<StoredUser | null>;
  /** Refresh-token grant. Reject with `{ fatal: true }` when the identity provider rejects the refresh token. */
  refresh(): Promise<StoredUser | null>;
  login(returnTo: string): Promise<void>;
  fetchImpl?: typeof fetch;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  marginSec?: number;
}

export type StreamStatus = "connecting" | "live" | "reconnecting";

export const isFatalRefresh = (e: unknown): boolean => !!e && typeof e === "object" && (e as { fatal?: boolean }).fatal === true;

export class AuthClient {
  private inflight: Promise<StoredUser> | null = null;
  private loginStarted = false;
  private readonly margin: number;

  constructor(private d: AuthDeps) {
    this.margin = d.marginSec ?? 60;
  }

  private now = () => (this.d.now ? this.d.now() : Date.now());
  private doFetch = (...a: Parameters<typeof fetch>) => (this.d.fetchImpl ?? fetch)(...a);
  private sleep = (ms: number) => (this.d.sleep ? this.d.sleep(ms) : new Promise<void>((r) => setTimeout(r, ms)));

  private stale(u: StoredUser): boolean {
    return u.expires_at !== undefined && u.expires_at * 1000 - this.now() <= this.margin * 1000;
  }

  /** One refresh at a time; concurrent callers share it. */
  private refreshShared(): Promise<StoredUser> {
    if (!this.inflight) {
      this.inflight = (async () => {
        try {
          const u = await this.d.refresh();
          if (!u) throw Object.assign(new Error("no session"), { fatal: true });
          return u;
        } catch (e) {
          if (isFatalRefresh(e)) {
            await this.requireLogin();
            throw new AuthRequired();
          }
          throw new ApiError(503, "authentication service unreachable; will retry");
        } finally {
          this.inflight = null;
        }
      })();
    }
    return this.inflight;
  }

  /** Redirect to the login page once, remembering where the user was. */
  async requireLogin(): Promise<void> {
    if (this.loginStarted) return;
    this.loginStarted = true;
    await this.d.login(typeof location !== "undefined" ? location.hash : "");
  }

  /** A token valid for at least the margin; refreshes first when needed. */
  async accessToken(): Promise<string> {
    const u = await this.d.getUser();
    if (u && !this.stale(u)) return u.access_token;
    return (await this.refreshShared()).access_token;
  }

  /** After a 401 for `failed`: reuse a token somebody else already renewed, otherwise renew once. */
  private async renewAfter401(failed: string): Promise<string> {
    const u = await this.d.getUser();
    if (u && u.access_token !== failed && !this.stale(u)) return u.access_token;
    return (await this.refreshShared()).access_token;
  }

  async api<T = unknown>(path: string, init: RequestInit = {}): Promise<T> {
    const send = (token: string) =>
      this.doFetch(path, {
        ...init,
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", ...(init.headers ?? {}) },
      });
    let token = await this.accessToken();
    let res = await send(token);
    if (res.status === 401) {
      token = await this.renewAfter401(token);
      res = await send(token); // exactly one retry: a second 401 is the answer
    }
    if (!res.ok) {
      let detail = res.statusText;
      try {
        detail = (await res.json()).detail ?? detail;
      } catch {
        /* not JSON */
      }
      throw new ApiError(res.status, detail);
    }
    return res.json();
  }

  /**
   * Server-Sent Events over fetch (EventSource cannot send an Authorization header). Reconnects forever with
   * exponential backoff (1 s .. 30 s, jittered) and a fresh token each time, until `signal` aborts.
   * The connection is also cycled shortly before its token would expire, so no stream outlives its credential.
   */
  async stream(path: string, onMessage: (data: string) => void, signal: AbortSignal, onStatus: (s: StreamStatus) => void = () => {}) {
    let attempt = 0;
    let consecutive401 = 0;
    const rnd = this.d.random ?? Math.random;
    while (!signal.aborted) {
      const conn = new AbortController();
      const onAbort = () => conn.abort();
      signal.addEventListener("abort", onAbort);
      let timer: ReturnType<typeof setTimeout> | undefined;
      const startedAt = this.now();
      try {
        onStatus(attempt === 0 && consecutive401 === 0 ? "connecting" : "reconnecting");
        const token = await this.accessToken();
        const res = await this.doFetch(path, { headers: { Authorization: `Bearer ${token}` }, signal: conn.signal });
        if (res.status === 401) {
          if (++consecutive401 > 1) {
            await this.requireLogin(); // a freshly renewed token was refused too: the session is not usable
            return;
          }
          await this.renewAfter401(token);
          continue; // reconnect immediately with the renewed token
        }
        if (!res.ok || !res.body) throw new Error(`stream HTTP ${res.status}`);
        consecutive401 = 0;
        onStatus("live");
        const u = await this.d.getUser();
        if (u?.expires_at !== undefined) {
          const ms = Math.max(1000, u.expires_at * 1000 - this.margin * 1000 - this.now());
          timer = setTimeout(() => conn.abort(), ms); // cycle before the credential expires
        }
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          let i: number;
          while ((i = buf.indexOf("\n\n")) >= 0) {
            const block = buf.slice(0, i);
            buf = buf.slice(i + 2);
            const data = block.split("\n").find((l) => l.startsWith("data: "));
            if (data) onMessage(data.slice(6));
          }
        }
      } catch (e) {
        if (signal.aborted || e instanceof AuthRequired) return;
      } finally {
        if (timer) clearTimeout(timer);
        signal.removeEventListener("abort", onAbort);
      }
      if (signal.aborted) return;
      // a connection that lived a while resets the backoff; one that dies at once does not (no connection storm)
      attempt = this.now() - startedAt > 10_000 ? 1 : attempt + 1;
      onStatus("reconnecting");
      const base = Math.min(30_000, 1000 * 2 ** Math.min(attempt - 1, 5));
      await this.sleep(base * (0.5 + rnd() * 0.5));
    }
  }
}
