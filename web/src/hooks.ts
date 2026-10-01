import { useCallback, useEffect, useRef, useState } from "react";

/** Runs `fn` now and every `ms`, exposing data/error/loading and a manual reload. */
export function usePoll<T>(fn: () => Promise<T>, ms: number | null, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const run = useCallback(async () => {
    try {
      setData(await fnRef.current());
      setError("");
    } catch (e: any) {
      setError(String(e?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    setLoading(true);
    run();
    if (!ms) return;
    const t = setInterval(run, ms);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms, run, ...deps]);
  return { data, error, loading, reload: run };
}

export const fmtTime = (s: string) => new Date(s).toLocaleString();
export const ago = (s: string) => {
  const d = (Date.now() - new Date(s).getTime()) / 1000;
  if (d < 90) return `${Math.max(0, Math.round(d))} s ago`;
  if (d < 5400) return `${Math.round(d / 60)} min ago`;
  return `${Math.round(d / 3600)} h ago`;
};
