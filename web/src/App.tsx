import { useEffect, useState } from "react";
import { api, currentUser, login, logout, setToken, type Me } from "./api";
import { Dashboard } from "./pages/Dashboard";
import { Vehicles } from "./pages/Vehicles";
import { Alerts } from "./pages/Alerts";
import { Chargers } from "./pages/Chargers";
import { Plans } from "./pages/Plans";
import { Reports } from "./pages/Reports";
import { Copilot } from "./pages/Copilot";
import { Audit } from "./pages/Audit";
import { Ops } from "./pages/Ops";

interface Page {
  id: string;
  label: string;
  perm: string;
  render: (me: Me) => JSX.Element;
}

const pages: Page[] = [
  { id: "dashboard", label: "Dashboard", perm: "fleet.read", render: (me) => <Dashboard me={me} /> },
  { id: "vehicles", label: "Vehicles", perm: "fleet.read", render: (me) => <Vehicles me={me} /> },
  { id: "alerts", label: "Alerts", perm: "alerts.read", render: (me) => <Alerts me={me} /> },
  { id: "chargers", label: "Chargers", perm: "chargers.read", render: (me) => <Chargers me={me} /> },
  { id: "plans", label: "Charge plans", perm: "plans.read", render: (me) => <Plans me={me} /> },
  { id: "reports", label: "Reports", perm: "reports.read", render: () => <Reports /> },
  { id: "copilot", label: "Copilot", perm: "copilot.use", render: () => <Copilot /> },
  { id: "audit", label: "Audit", perm: "audit.read", render: () => <Audit /> },
  { id: "ops", label: "Platform", perm: "ops.read", render: () => <Ops /> },
];

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [state, setState] = useState<"loading" | "anon" | "ready" | "error">("loading");
  const [error, setError] = useState("");
  const [page, setPage] = useState(() => window.location.hash.slice(1) || "dashboard");

  useEffect(() => {
    (async () => {
      try {
        const u = await currentUser();
        if (!u) return setState("anon");
        setToken(u.access_token);
        setMe(await api<Me>("/v1/me"));
        setState("ready");
      } catch (e: any) {
        setError(String(e?.message ?? e));
        setState("error");
      }
    })();
  }, []);

  useEffect(() => {
    const h = () => setPage(window.location.hash.slice(1) || "dashboard");
    window.addEventListener("hashchange", h);
    return () => window.removeEventListener("hashchange", h);
  }, []);

  if (state === "loading") return <div className="center">Loading…</div>;
  if (state === "error")
    return (
      <div className="center">
        <p className="err">Could not start: {error}</p>
        <button onClick={() => login()}>Sign in again</button>
      </div>
    );
  if (state === "anon" || !me)
    return (
      <div className="center">
        <h1>⚡ VoltSight</h1>
        <p>Fleet range-risk &amp; charge orchestration</p>
        <button className="primary" onClick={() => login()}>
          Sign in
        </button>
      </div>
    );

  const visible = pages.filter((p) => me.permissions.includes(p.perm));
  const current = visible.find((p) => p.id === page) ?? visible[0];
  return (
    <div className="shell">
      <nav>
        <div className="brand">⚡ VoltSight</div>
        {visible.map((p) => (
          <a key={p.id} href={`#${p.id}`} className={p.id === current?.id ? "active" : ""}>
            {p.label}
          </a>
        ))}
        <div className="spacer" />
        <div className="who">
          <div>{me.roles.join(", ")}</div>
          <div className="muted small">tenant {me.tenant_id.slice(0, 8)}</div>
          <button onClick={() => logout()}>Sign out</button>
        </div>
      </nav>
      <main>{current ? current.render(me) : <p>No pages are available for your role.</p>}</main>
    </div>
  );
}
