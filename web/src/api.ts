import { UserManager, WebStorageStateStore, ErrorResponse, type User } from "oidc-client-ts";
import { AuthClient, type StreamStatus } from "./auth";

export { ApiError } from "./auth";
export type { StreamStatus } from "./auth";

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
    response_type: "code", // authorization code + PKCE, unchanged
    scope: "openid profile",
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
    // renewal is driven by AuthClient (single-flight, margin, retry-once); the library only performs the grant
    automaticSilentRenew: false,
  });
  return manager;
}

const toStored = (u: User | null) => (u ? { access_token: u.access_token, expires_at: u.expires_at } : null);

/** Refresh-token grant. A rejection by Keycloak (invalid_grant: the SSO session ended) is fatal; anything else is transient. */
async function refreshGrant() {
  const m = await getManager();
  try {
    return toStored(await m.signinSilent());
  } catch (e) {
    if (e instanceof ErrorResponse) throw Object.assign(e, { fatal: true });
    throw e;
  }
}

export const auth = new AuthClient({
  getUser: async () => toStored(await (await getManager()).getUser()),
  refresh: refreshGrant,
  login: async (returnTo) => (await getManager()).signinRedirect({ state: { hash: returnTo } }),
});

/** The signed-in user, completing a pending login redirect first; a stored session whose access token has
 *  expired is renewed with its refresh token instead of being discarded (page reloads keep the session). */
export async function currentUser(): Promise<User | null> {
  const m = await getManager();
  if (window.location.search.includes("code=") && window.location.search.includes("state=")) {
    const u = await m.signinRedirectCallback();
    const back = (u.state as { hash?: string } | undefined)?.hash ?? "";
    window.history.replaceState({}, document.title, "/" + back);
    return u;
  }
  const u = await m.getUser();
  if (u && !u.expired) return u;
  if (!u) return null;
  try {
    return await m.signinSilent();
  } catch {
    return null; // the SSO session is really gone: show Sign in
  }
}

export async function login() {
  await (await getManager()).signinRedirect({ state: { hash: window.location.hash } });
}
export async function logout() {
  await (await getManager()).signoutRedirect();
}

export const api = <T = any>(path: string, init: RequestInit = {}): Promise<T> => auth.api<T>(path, init);

/** Live alert stream; `onStatus` reports connecting / live / reconnecting for the connection indicator. */
export function streamAlerts(onAlert: (a: any) => void, signal: AbortSignal, onStatus?: (s: StreamStatus) => void) {
  void auth.stream(
    "/v1/alerts/stream",
    (d) => {
      try {
        onAlert(JSON.parse(d));
      } catch {
        /* ignore a malformed event */
      }
    },
    signal,
    onStatus,
  );
}
