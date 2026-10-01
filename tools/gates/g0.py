"""Gate G0 runner: toolchain, base infrastructure health, functional probes, memory baseline.

Writes evidence/G0/<UTC>-<git sha>/{env.json,results.json,run.log}. Exit code 0 only if every
criterion passes. Nothing here is mocked: each probe talks to the real service.
"""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import hmac
import json
import os
import pathlib
import platform
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
COMPOSE = ["docker", "compose", "-f", str(ROOT / "deploy/compose/docker-compose.yml"),
           "--env-file", str(ROOT / ".env")]
CORE_SERVICES = ["kafka", "postgres", "clickhouse", "redis", "objectstore", "keycloak", "vault",
                 "otel-collector", "prometheus", "grafana"]
HEALTHCHECK_LESS = {"otel-collector"}  # distroless image: probed over HTTP instead

TOOLS = {
    "docker": ["docker", "--version"],
    "docker compose": ["docker", "compose", "version"],
    "go": ["go", "version"],
    "kubectl": ["kubectl", "version", "--client"],
    "helm": ["helm", "version", "--short"],
    "kind": ["kind", "version"],
    "terraform": ["terraform", "version"],
    "k6": ["k6", "version"],
    "buf": ["buf", "--version"],
    "gh": ["gh", "--version"],
    "trivy": ["trivy", "--version"],
    "syft": ["syft", "version"],
    "gitleaks": ["gitleaks", "version"],
    "hadolint": ["hadolint", "--version"],
    "tflint": ["tflint", "--version"],
    "python": [sys.executable, "--version"],
    "node": ["node", "--version"],
}

LOG: list[str] = []


def log(msg: str) -> None:
    line = f"[{dt.datetime.now(dt.timezone.utc).strftime('%H:%M:%S')}] {msg}"
    LOG.append(line)
    print(line, flush=True)


def run(cmd: list[str], timeout: int = 120, check: bool = False) -> subprocess.CompletedProcess:
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, cwd=ROOT)
    if check and p.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)} -> {p.returncode}: {p.stderr.strip()[:500]}")
    return p


def git_sha() -> tuple[str, bool]:
    p = run(["git", "rev-parse", "--short", "HEAD"])
    sha = p.stdout.strip() if p.returncode == 0 else "nocommit"
    dirty = bool(run(["git", "status", "--porcelain"]).stdout.strip())
    return sha, dirty


def load_env() -> dict[str, str]:
    env = {}
    for line in (ROOT / ".env").read_text().splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            env[k] = v
    return env


def http(url: str, method: str = "GET", data: bytes | None = None, headers: dict | None = None,
         timeout: int = 10) -> tuple[int, bytes]:
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def collect_env() -> dict:
    versions = {}
    for name, cmd in TOOLS.items():
        exe = shutil.which(cmd[0])
        if exe is None:
            versions[name] = None
            continue
        p = run(cmd, timeout=60)
        out = (p.stdout or p.stderr).strip().splitlines()
        versions[name] = out[0].strip() if out else "?"
    info = json.loads(run(["docker", "info", "--format", "{{json .}}"]).stdout)
    total, _, free = shutil.disk_usage(ROOT.anchor or "/")
    sha, dirty = git_sha()
    return {
        "utc": dt.datetime.now(dt.timezone.utc).isoformat(),
        "git": {"sha": sha, "dirty": dirty},
        "host": {"os": platform.platform(), "cpu_logical": os.cpu_count(),
                 "disk_total_gb": round(total / 2**30, 1), "disk_free_gb": round(free / 2**30, 1)},
        "docker_engine": {"server_version": info.get("ServerVersion"), "ncpu": info.get("NCPU"),
                          "mem_gb": round(info.get("MemTotal", 0) / 2**30, 2)},
        "tool_versions": versions,
    }


# ----------------------------------------------------------------------------- probes
def probe_kafka() -> str:
    topic = f"g0.probe.{int(time.time())}"
    run(COMPOSE + ["exec", "-T", "kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server",
                   "localhost:9092", "--create", "--topic", topic, "--partitions", "3"], check=True)
    msg = f"hello-{time.time_ns()}"
    p = subprocess.run(COMPOSE + ["exec", "-T", "kafka", "/opt/kafka/bin/kafka-console-producer.sh",
                                   "--bootstrap-server", "localhost:9092", "--topic", topic],
                       input=msg + "\n", capture_output=True, text=True, timeout=60, cwd=ROOT)
    if p.returncode != 0:
        raise RuntimeError(p.stderr[:300])
    c = run(COMPOSE + ["exec", "-T", "kafka", "/opt/kafka/bin/kafka-console-consumer.sh",
                       "--bootstrap-server", "localhost:9092", "--topic", topic, "--from-beginning",
                       "--max-messages", "1", "--timeout-ms", "20000"], timeout=60)
    if msg not in c.stdout:
        raise RuntimeError(f"round trip failed: {c.stdout!r} {c.stderr[-200:]!r}")
    return "produce/consume round-trip ok on 3-partition topic"


def probe_postgres() -> str:
    q = ("CREATE EXTENSION IF NOT EXISTS vector; "
         "SELECT (SELECT extversion FROM pg_extension WHERE extname='vector') || ' / keycloak_db=' || "
         "(SELECT count(*) FROM pg_database WHERE datname='keycloak');")
    p = run(COMPOSE + ["exec", "-T", "postgres", "psql", "-U", "voltsight", "-d", "voltsight",
                       "-Atc", q], check=True)
    out = p.stdout.strip().splitlines()[-1]
    if not out.endswith("keycloak_db=1"):
        raise RuntimeError(out)
    return f"pgvector {out}"


def probe_clickhouse(env: dict) -> str:
    p = run(COMPOSE + ["exec", "-T", "clickhouse", "clickhouse-client", "--user", "voltsight",
                       "--password", env["CLICKHOUSE_PASSWORD"], "-q",
                       "SELECT version(), sum(number) FROM numbers(1000000)"], check=True)
    return p.stdout.strip().replace("\t", " ")


def probe_redis(env: dict) -> str:
    p = run(COMPOSE + ["exec", "-T", "redis", "redis-cli", "-a", env["REDIS_PASSWORD"],
                       "--no-auth-warning", "SET", "g0:probe", "ok"], check=True)
    g = run(COMPOSE + ["exec", "-T", "redis", "redis-cli", "-a", env["REDIS_PASSWORD"],
                       "--no-auth-warning", "GET", "g0:probe"], check=True)
    if g.stdout.strip() != "ok":
        raise RuntimeError(g.stdout)
    unauth = run(COMPOSE + ["exec", "-T", "redis", "redis-cli", "PING"])
    if "NOAUTH" not in unauth.stdout + unauth.stderr:
        raise RuntimeError("redis accepted an unauthenticated PING")
    return f"auth enforced (unauthenticated PING rejected); {p.stdout.strip()}"


def _sigv4(method: str, path: str, body: bytes, access: str, secret: str, host: str) -> dict:
    now = dt.datetime.now(dt.timezone.utc)
    amz, day, region, service = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d"), "us-east-1", "s3"
    payload_hash = hashlib.sha256(body).hexdigest()
    headers = {"host": host, "x-amz-content-sha256": payload_hash, "x-amz-date": amz}
    signed = ";".join(sorted(headers))
    canon = "\n".join([method, path, "", "".join(f"{k}:{headers[k]}\n" for k in sorted(headers)),
                       signed, payload_hash])
    scope = f"{day}/{region}/{service}/aws4_request"
    sts = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canon.encode()).hexdigest()])

    def h(k: bytes, m: str) -> bytes:
        return hmac.new(k, m.encode(), hashlib.sha256).digest()

    key = h(h(h(h(("AWS4" + secret).encode(), day), region), service), "aws4_request")
    sig = hmac.new(key, sts.encode(), hashlib.sha256).hexdigest()
    headers["Authorization"] = (f"AWS4-HMAC-SHA256 Credential={access}/{scope}, "
                                f"SignedHeaders={signed}, Signature={sig}")
    return headers


def probe_s3(env: dict) -> str:
    host = "localhost:8333"
    bucket, key, body = "g0-probe", "g0-probe/object.txt", f"parquet-stand-in-{time.time_ns()}".encode()
    a, s = env["S3_ACCESS_KEY"], env["S3_SECRET_KEY"]
    st, _ = http(f"http://{host}/{bucket}", "PUT", b"", _sigv4("PUT", f"/{bucket}", b"", a, s, host))
    if st not in (200, 409):
        raise RuntimeError(f"create bucket -> {st}")
    st, _ = http(f"http://{host}/{key}", "PUT", body, _sigv4("PUT", f"/{key}", body, a, s, host))
    if st != 200:
        raise RuntimeError(f"put -> {st}")
    st, got = http(f"http://{host}/{key}", "GET", None, _sigv4("GET", f"/{key}", b"", a, s, host))
    if st != 200 or got != body:
        raise RuntimeError(f"get -> {st} body mismatch")
    st, _ = http(f"http://{host}/{key}", "GET")
    if st not in (401, 403):
        raise RuntimeError(f"anonymous GET returned {st}, expected 401/403")
    return "signed bucket create/put/get ok; anonymous read rejected"


def probe_keycloak() -> str:
    st, body = http("http://localhost:8080/realms/master/.well-known/openid-configuration")
    doc = json.loads(body)
    if st != 200 or "jwks_uri" not in doc:
        raise RuntimeError(f"{st} {body[:200]!r}")
    return f"issuer={doc['issuer']}"


def probe_vault() -> str:
    st, body = http("http://localhost:8200/v1/sys/health")
    d = json.loads(body)
    if st != 200 or not d.get("initialized") or d.get("sealed"):
        raise RuntimeError(body[:200])
    return f"vault {d['version']} initialized, unsealed (dev mode)"


def probe_otel_to_prometheus() -> str:
    st, _ = http("http://localhost:13133/")
    if st != 200:
        raise RuntimeError(f"collector health {st}")
    name = f"g0_probe_{int(time.time())}"
    now = time.time_ns()
    payload = {"resourceMetrics": [{"resource": {"attributes": [
        {"key": "service.name", "value": {"stringValue": "g0-probe"}}]},
        "scopeMetrics": [{"metrics": [{"name": name, "gauge": {"dataPoints": [
            {"asDouble": 42.0, "timeUnixNano": str(now)}]}}]}]}]}
    st, body = http("http://localhost:4318/v1/metrics", "POST", json.dumps(payload).encode(),
                    {"Content-Type": "application/json"})
    if st != 200:
        raise RuntimeError(f"OTLP export {st} {body[:200]!r}")
    deadline = time.time() + 45
    while time.time() < deadline:
        _, b = http(f"http://localhost:9090/api/v1/query?query={name}")
        res = json.loads(b)["data"]["result"]
        if res and float(res[0]["value"][1]) == 42.0:
            return f"OTLP metric {name} visible in Prometheus with value 42"
        time.sleep(2)
    raise RuntimeError("metric never reached Prometheus")


def probe_prometheus_targets() -> str:
    _, b = http("http://localhost:9090/api/v1/targets")
    targets = json.loads(b)["data"]["activeTargets"]
    state = {t["labels"]["job"]: t["health"] for t in targets}
    bad = {j: h for j, h in state.items() if h != "up"}
    if bad:
        raise RuntimeError(f"targets not up: {bad}")
    return f"targets up: {sorted(state)}"


def probe_grafana(env: dict) -> str:
    auth = base64.b64encode(f"admin:{env['GRAFANA_ADMIN_PASSWORD']}".encode()).decode()
    st, body = http("http://localhost:3000/api/datasources/uid/prometheus/health",
                    headers={"Authorization": f"Basic {auth}"})
    d = json.loads(body)
    if st != 200 or d.get("status") != "OK":
        raise RuntimeError(f"{st} {body[:200]!r}")
    return "Prometheus datasource healthy through Grafana"


def mem_snapshot() -> dict[str, float]:
    p = run(["docker", "stats", "--no-stream", "--format", "{{json .}}"], timeout=120, check=True)
    out = {}
    for line in p.stdout.splitlines():
        d = json.loads(line)
        if not d["Name"].startswith("voltsight-"):
            continue
        used = d["MemUsage"].split("/")[0].strip()
        m = re.match(r"([\d.]+)\s*([KMG]i?B)", used)
        mult = {"KiB": 1 / 1024, "MiB": 1, "GiB": 1024, "KB": 1 / 1024, "MB": 1, "GB": 1024}[m.group(2)]
        out[d["Name"].removeprefix("voltsight-").rsplit("-", 1)[0]] = round(float(m.group(1)) * mult, 1)
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--clean", action="store_true", help="down -v first and time a cold start")
    ap.add_argument("--no-evidence", action="store_true")
    args = ap.parse_args()

    run([sys.executable, str(ROOT / "tools/dev/gen_env.py")])
    env_file = load_env()
    criteria: dict[str, dict] = {}

    def crit(cid: str, ok: bool, detail) -> None:
        criteria[cid] = {"status": "PASS" if ok else "FAIL", "detail": detail}
        log(f"{cid}: {'PASS' if ok else 'FAIL'} - {detail}")

    env = collect_env()
    missing = [k for k, v in env["tool_versions"].items() if not v]
    crit("G0.1", not missing, "missing: " + ", ".join(missing) if missing else "all tools report a version")
    de = env["docker_engine"]
    crit("G0.2", de["mem_gb"] >= 11 and de["ncpu"] >= 12, f"docker mem={de['mem_gb']} GB ncpu={de['ncpu']}")
    crit("G0.3", True, env["host"])

    t_healthy = None
    if args.clean:
        log("clean cold start: compose down -v")
        run(COMPOSE + ["down", "-v", "--remove-orphans"], timeout=300)
    t0 = time.time()
    p = run(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "300"], timeout=420)
    t_healthy = round(time.time() - t0, 1)
    log(p.stderr.strip()[-600:] if p.returncode else "compose up --wait returned success")
    ps = run(COMPOSE + ["ps", "--format", "json"], check=True).stdout
    rows = [json.loads(line) for line in ps.splitlines() if line.strip()]
    state = {r["Service"]: (r["State"], r.get("Health", "")) for r in rows}
    unhealthy = [s for s in CORE_SERVICES if s not in state or state[s][0] != "running" or
                 (s not in HEALTHCHECK_LESS and state[s][1] != "healthy")]
    crit("G0.4", p.returncode == 0 and not unhealthy,
         {"time_to_healthy_s": t_healthy, "cold_start": args.clean, "services": state,
          "unhealthy": unhealthy})

    probes = {}
    for name, fn in [("kafka", probe_kafka), ("postgres", probe_postgres),
                     ("clickhouse", lambda: probe_clickhouse(env_file)),
                     ("redis", lambda: probe_redis(env_file)), ("s3", lambda: probe_s3(env_file)),
                     ("keycloak", probe_keycloak), ("vault", probe_vault),
                     ("grafana", lambda: probe_grafana(env_file))]:
        try:
            probes[name] = {"ok": True, "detail": fn()}
        except Exception as e:  # noqa: BLE001 - report every probe failure
            probes[name] = {"ok": False, "detail": f"{type(e).__name__}: {e}"}
        log(f"probe {name}: {probes[name]}")
    crit("G0.5", all(v["ok"] for v in probes.values()), probes)

    try:
        d7 = probe_otel_to_prometheus()
        d7b = probe_prometheus_targets()
        crit("G0.7", True, f"{d7}; {d7b}")
    except Exception as e:  # noqa: BLE001
        crit("G0.7", False, f"{type(e).__name__}: {e}")

    log("settling 30 s before memory sample")
    time.sleep(30)
    mem = mem_snapshot()
    total = round(sum(mem.values()), 1)
    crit("G0.6", bool(mem), {"idle_mem_mb": mem, "total_mb": total})

    sha, dirty = git_sha()
    crit("G0.10", ".env" in run(["git", "check-ignore", ".env"]).stdout, ".env is git-ignored")
    overall = "PASS" if all(c["status"] == "PASS" for c in criteria.values()) else "FAIL"
    results = {"gate": "G0", "status": overall, "git": {"sha": sha, "dirty": dirty}, "criteria": criteria,
               "not_covered_here": ["G0.8 (static checks) and G0.9 (CI) are verified by CI, see evidence/G0/ci.md"]}
    log(f"G0 (local portion): {overall}")
    if not args.no_evidence:
        out = ROOT / "evidence/G0" / f"{dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')}-{sha}"
        out.mkdir(parents=True, exist_ok=True)
        (out / "env.json").write_text(json.dumps(env, indent=2))
        (out / "results.json").write_text(json.dumps(results, indent=2, default=str))
        (out / "run.log").write_text("\n".join(LOG))
        log(f"evidence written to {out.relative_to(ROOT)}")
    return 0 if overall == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
