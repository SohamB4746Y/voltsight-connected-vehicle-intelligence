import { useRef, useState } from "react";
import { api } from "../api";

interface Msg {
  role: "user" | "assistant";
  text: string;
  citations?: string[];
  pending?: { id: string; tool: string; summary: string }[];
  tools?: { tool: string; ok: boolean }[];
}

export function Copilot() {
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const session = useRef<string>("");
  const send = async (text: string) => {
    if (!text.trim() || busy) return;
    setMsgs((m) => [...m, { role: "user", text }]);
    setInput("");
    setBusy(true);
    try {
      const r = await api("/v1/copilot/messages", { method: "POST", body: JSON.stringify({ session_id: session.current || undefined, message: text }) });
      session.current = r.session_id;
      setMsgs((m) => [...m, { role: "assistant", text: r.answer, citations: r.citations, pending: r.pending_actions, tools: r.tool_calls }]);
    } catch (e: any) {
      setMsgs((m) => [...m, { role: "assistant", text: `⚠ ${e.message}` }]);
    } finally {
      setBusy(false);
    }
  };
  const approve = async (id: string) => {
    try {
      const r = await api(`/v1/copilot/actions/${id}/approve`, { method: "POST" });
      setMsgs((m) => [...m, { role: "assistant", text: r.message }]);
    } catch (e: any) {
      setMsgs((m) => [...m, { role: "assistant", text: `⚠ ${e.message}` }]);
    }
  };
  const examples = ["Which vehicles are at risk right now?", "Why is the most critical vehicle at risk?", "Find similar past incidents", "Propose a charging plan for tonight"];
  return (
    <div className="chat">
      <h2>Charge Ops Copilot</h2>
      <p className="muted">Answers come only from tool calls on your tenant's data (with your permissions); every step is audited. Writes need your approval.</p>
      <div className="log">
        {msgs.length === 0 && <div className="row wrap">{examples.map((e) => <button key={e} onClick={() => send(e)}>{e}</button>)}</div>}
        {msgs.map((m, i) => (
          <div key={i} className={`msg ${m.role}`}>
            <div className="text">{m.text}</div>
            {m.citations && m.citations.length > 0 && <div className="muted small">sources: {m.citations.join(", ")}</div>}
            {m.tools && m.tools.length > 0 && <div className="muted small">tools: {m.tools.map((t) => `${t.tool}${t.ok ? "" : " ✗"}`).join(" → ")}</div>}
            {m.pending?.map((p) => (
              <div key={p.id} className="box">
                Proposed action: <b>{p.tool}</b> — {p.summary} <button className="primary" onClick={() => approve(p.id)}>Approve</button>
              </div>
            ))}
          </div>
        ))}
        {busy && <div className="muted">thinking…</div>}
      </div>
      <form onSubmit={(e) => { e.preventDefault(); send(input); }} className="row">
        <input value={input} onChange={(e) => setInput(e.target.value)} placeholder="Ask about at-risk vehicles, alerts, chargers, plans…" />
        <button className="primary" disabled={busy}>Send</button>
      </form>
    </div>
  );
}
