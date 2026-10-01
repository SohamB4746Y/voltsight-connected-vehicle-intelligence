import { UserManager, WebStorageStateStore, type User } from "oidc-client-ts";

export interface Me {
  user_id: string;
  tenant_id: string;
  roles: string[];
  permissions: string[];
}

let manager: UserManager | null = null;

async function getManager(): Promise<UserManager> {
  if (manager) return manager;
  const cfg = await (await fetch("/v1/config")).json();
  manager = new UserManager({
    authority: cfg.issuer,
    client_id: cfg.client_id,
    redirect_uri: window.location.origin + "/",
    post_logout_redirect_uri: window.location.origin + "/",
    response_type: "code", // authorization code + PKCE
    scope: "openid profile",
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
    automaticSilentRenew: false,
  });
  return manager;
}

export async function currentUser(): Promise<User | null> {
  const m = await getManager();
  if (window.location.search.includes("code=") && window.location.search.includes("state=")) {
    const u = await m.signinRedirectCallback();
    window.history.replaceState({}, document.title, "/");
    return u;
  }
  const u = await m.getUser();
  return u && !u.expired ? u : null;
}

export async function login() {
  await (await getManager()).signinRedirect();
}
export async function logout() {
  await (await getManager()).signoutRedirect();
}

let token = "";
export function setToken(t: string) {
  token = t;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T = any>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", ...(init.headers ?? {}) },
  });
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

/** Server-Sent Events over fetch (EventSource cannot send an Authorization header). */
export function streamAlerts(onAlert: (a: any) => void, signal: AbortSignal) {
  (async () => {
    try {
      const res = await fetch("/v1/alerts/stream", { headers: { Authorization: `Bearer ${token}` }, signal });
      if (!res.ok || !res.body) return;
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n\n")) >= 0) {
          const block = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const data = block.split("\n").find((l) => l.startsWith("data: "));
          if (data) onAlert(JSON.parse(data.slice(6)));
        }
      }
    } catch {
      /* aborted or disconnected */
    }
  })();
}
