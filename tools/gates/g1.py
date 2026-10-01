"""Gate G1 runner: contracts, PostgreSQL core, tenant isolation, identity, device PKI, 100K seed.

Writes evidence/G1/<UTC>-<git sha>/. Exit code 0 only if every criterion passes. Everything runs for
real: Testcontainers PostgreSQL, the compose stack's Vault and Keycloak, Semgrep in a container.
"""
from __future__ import annotations

import datetime as dt
import hashlib
import json
import os
import pathlib
import platform
import shutil
import subprocess
import sys
import tempfile
import time
from collections import defaultdict

ROOT = pathlib.Path(__file__).resolve().parents[2]
LOG: list[str] = []
CRIT: dict[str, dict] = {}
GATE_PKGS = ["vin", "seedgen", "pki", "jwtverify", "realm", "geo", "dotenv", "dbtool", "erdiagram"]
COVERAGE_MIN = 80.0


def log(msg: str) -> None:
    line = f"[{dt.datetime.now(dt.timezone.utc).strftime('%H:%M:%S')}] {msg}"
    LOG.append(line)
    print(line, flush=True)


def run(cmd, env=None, timeout=1800, cwd=ROOT, capture=True):
    e = os.environ.copy()
    e.update(env or {})
    # explicit UTF-8: tool output (e.g. Testcontainers logs) contains non-cp1252 characters on Windows
    return subprocess.run(cmd, capture_output=capture, text=True, encoding="utf-8", errors="replace",
                          timeout=timeout, cwd=cwd, env=e)


def crit(cid: str, ok: bool, detail) -> None:
    CRIT[cid] = {"status": "PASS" if ok else "FAIL", "detail": detail}
    log(f"{cid}: {'PASS' if ok else 'FAIL'} - {json.dumps(detail, default=str)[:600]}")


def git_sha() -> tuple[str, bool]:
    p = run(["git", "rev-parse", "--short", "HEAD"])
    sha = p.stdout.strip() if p.returncode == 0 else "nocommit"
    return sha, bool(run(["git", "status", "--porcelain"]).stdout.strip())


def tree_hash(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    for f in sorted(path.rglob("*")):
        if f.is_file():
            h.update(f.relative_to(path).as_posix().encode())
            h.update(f.read_bytes())
    return h.hexdigest()


# ------------------------------------------------------------------------------------------ G1.1
def check_proto(out: pathlib.Path) -> None:
    res: dict = {}
    proto = ROOT / "proto"
    res["lint"] = run(["buf", "lint"], cwd=proto).returncode == 0
    res["format_clean"] = run(["buf", "format", "--diff", "--exit-code"], cwd=proto).returncode == 0
    before = tree_hash(ROOT / "gen")
    gen = run(["buf", "generate"], cwd=proto)
    res["generate_ok"] = gen.returncode == 0
    res["generated_code_up_to_date"] = tree_hash(ROOT / "gen") == before

    # positive: no breaking change versus the baseline (default HEAD, i.e. uncommitted edits are checked;
    # CI sets BUF_BASELINE_REF=HEAD~1 so the commit under test is compared with its parent)
    ref = os.environ.get("BUF_BASELINE_REF", "HEAD")
    has_baseline = run(["git", "cat-file", "-e", f"{ref}:proto/buf.yaml"]).returncode == 0
    res["baseline_ref"] = ref
    if has_baseline:
        # run inside the module directory (proto/ holds buf.yaml); the baseline is that same subdir at `ref`
        res["breaking_vs_baseline"] = run(["buf", "breaking", "--against", f"../.git#ref={ref},subdir=proto"], cwd=proto).returncode == 0
    else:
        res["breaking_vs_baseline"] = f"no baseline at {ref} yet (first contract commit); detection capability proven below"

    # negative: a deliberately breaking mutation MUST be detected
    with tempfile.TemporaryDirectory() as td:
        a, b = pathlib.Path(td) / "base", pathlib.Path(td) / "mutated"
        shutil.copytree(proto, a)
        shutil.copytree(proto, b)
        f = b / "voltsight/telemetry/v1/telemetry.proto"
        text = f.read_text(encoding="utf-8")
        assert "uint64 seq = 10;" in text
        f.write_text(text.replace("uint64 seq = 10;", "string seq = 10;").replace("float soc_pct = 6;", ""), encoding="utf-8")
        r = run(["buf", "breaking", str(b), "--against", str(a)], cwd=ROOT)
        res["negative_test"] = {"breaking_change_detected": r.returncode != 0,
                                "findings": [l for l in r.stdout.splitlines() if l.strip()][:6]}
        # and a non-breaking addition must NOT be flagged
        c = pathlib.Path(td) / "additive"
        shutil.copytree(proto, c)
        g = c / "voltsight/telemetry/v1/telemetry.proto"
        g.write_text(g.read_text(encoding="utf-8").replace("string tenant_id = 30;", "string tenant_id = 30;\n  string extra_new_field = 99;"), encoding="utf-8")
        res["negative_test"]["additive_change_allowed"] = run(["buf", "breaking", str(c), "--against", str(a)], cwd=ROOT).returncode == 0
    (out / "proto.json").write_text(json.dumps(res, indent=2), encoding="utf-8")
    ok = (res["lint"] and res["format_clean"] and res["generate_ok"] and res["generated_code_up_to_date"]
          and res["negative_test"]["breaking_change_detected"] and res["negative_test"]["additive_change_allowed"]
          and (res["breaking_vs_baseline"] is True or isinstance(res["breaking_vs_baseline"], str)))
    crit("G1.1", bool(ok), res)


# ------------------------------------------------------------------------------- G1.2..G1.10 + G1.11 coverage
def parse_go_json(lines: list[str]) -> dict:
    tests: dict[str, str] = {}
    pkgs: dict[str, str] = {}
    for line in lines:
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        act, pkg, test = ev.get("Action"), ev.get("Package"), ev.get("Test")
        if act in ("pass", "fail", "skip"):
            if test:
                tests[f"{pkg}::{test}"] = act
            else:
                pkgs[pkg] = act
    return {"tests": tests, "packages": pkgs}


def coverage_by_package(profile: pathlib.Path) -> dict[str, float]:
    covered: dict[tuple, int] = {}
    stmts: dict[tuple, int] = {}
    for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
        loc, n, cnt = line.rsplit(" ", 2)
        key = loc
        stmts[key] = int(n)
        covered[key] = max(covered.get(key, 0), int(cnt))
    tot, cov = defaultdict(int), defaultdict(int)
    for loc, n in stmts.items():
        pkg = loc.rsplit("/", 1)[0].removeprefix("voltsight/")
        tot[pkg] += n
        if covered[loc] > 0:
            cov[pkg] += n
    return {p: round(100 * cov[p] / tot[p], 1) for p in tot}


def run_go_tests(out: pathlib.Path) -> None:
    env = {"VOLTSIGHT_EVIDENCE_DIR": str(out)}
    if platform.system() == "Windows":
        # Ryuk (the Testcontainers reaper) stays enabled; on Docker Desktop for Windows it must mount the
        # Linux-side socket path instead of the named pipe. Not needed (and not set) on Linux CI.
        env["TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE"] = "//var/run/docker.sock"
    prof = out / "coverage.out"
    log("go test (unit + integration[Testcontainers] + stack[Vault, Keycloak]) ...")
    t0 = time.time()
    p = run(["go", "test", "-tags", "integration stack", "-count=1", "-json", "-p", "1",
             f"-coverprofile={prof}", "-coverpkg=./internal/...,./db/...", "./..."], env=env, timeout=3000)
    dur = round(time.time() - t0, 1)
    (out / "go-test.json").write_text(p.stdout, encoding="utf-8")
    parsed = parse_go_json(p.stdout.splitlines())
    tests = parsed["tests"]
    passed = sum(1 for v in tests.values() if v == "pass")
    failed = [k for k, v in tests.items() if v == "fail"]
    skipped = [k for k, v in tests.items() if v == "skip"]
    summary = {"duration_s": dur, "tests_passed": passed, "tests_failed": failed, "tests_skipped": skipped,
               "packages": parsed["packages"], "exit_code": p.returncode}
    log(f"go test finished: {passed} passed, {len(failed)} failed, {len(skipped)} skipped in {dur}s")

    def tests_in(prefix: str, names: list[str] | None = None) -> bool:
        sel = {k: v for k, v in tests.items() if k.split("::")[0].endswith(prefix) and (not names or any(n in k for n in names))}
        return bool(sel) and all(v == "pass" for v in sel.values())

    crit("G1.2", tests_in("internal/contract"), {"tests": {k: v for k, v in tests.items() if "internal/contract" in k}})
    crit("G1.3", tests_in("db/dbtests", ["TestMigrationsUpDownUp"]), "up -> down -> up on a fresh PostgreSQL; fingerprint equality asserted in the test")
    crit("G1.4", tests_in("db/dbtests", ["TestSchemaLint"]), "schema lint tests")
    crit("G1.5", tests_in("db/dbtests", ["TestRLS_", "TestKnownLimitation"]), "RLS attack suite (+ documented limitation assertion)")
    crit("G1.6", tests_in("db/dbtests", ["TestACID_"]), "ACID suite")
    seed_ok = tests_in("db/dbtests", ["TestSeed"]) and (out / "seed_run.json").exists()
    crit("G1.7", seed_ok, json.loads((out / "seed_run.json").read_text(encoding="utf-8"))["load"] if (out / "seed_run.json").exists() else "seed_run.json missing")
    crit("G1.8", tests_in("internal/jwtverify") and tests_in("internal/realm"), "Keycloak realm + JWT verification against the running Keycloak")
    crit("G1.9", tests_in("internal/pki"), "Vault PKI against the running Vault")
    crit("G1.10", tests_in("db/dbtests", ["TestERDiagram", "TestNormalFormDocument"]), "ER regenerated from live DDL; 3NF doc covers every table")

    cov = coverage_by_package(prof) if prof.exists() else {}
    gate_cov = {p: cov.get(f"internal/{p}") for p in GATE_PKGS}
    low = {p: c for p, c in gate_cov.items() if c is None or c < COVERAGE_MIN}
    summary["coverage_pct"] = gate_cov
    summary["coverage_min_required"] = COVERAGE_MIN
    (out / "coverage.txt").write_text(json.dumps(gate_cov, indent=2), encoding="utf-8")
    (out / "go-test-summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
    crit("G1.11a_tests", p.returncode == 0 and not failed and not skipped, {k: summary[k] for k in ("tests_passed", "tests_failed", "tests_skipped", "exit_code")})
    crit("G1.11b_coverage", not low, {"coverage_pct": gate_cov, "below_threshold": low})


# ------------------------------------------------------------------------------------- mutation check
def mutation_check(out: pathlib.Path) -> None:
    """Weaken the tenant policy and prove the RLS suite notices; then restore the file byte-for-byte."""
    f = ROOT / "db/migrations/000003_rls_and_grants.up.sql"
    original = f.read_bytes()
    needle = (b"USING (tenant_id = (SELECT app_tenant()))\n                      "
              b"WITH CHECK (tenant_id = (SELECT app_tenant()))")
    if needle not in original:
        crit("G1.5_mutation", False, "policy text not found; update the mutation needle")
        return
    env = {}
    if platform.system() == "Windows":
        env["TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE"] = "//var/run/docker.sock"
    try:
        f.write_bytes(original.replace(needle, b"USING (true) WITH CHECK (true)"))
        p = run(["go", "test", "-tags", "integration", "-count=1", "-run", "TestRLS_", "./db/dbtests/"], env=env, timeout=900)
    finally:
        f.write_bytes(original)
    restored = f.read_bytes() == original
    leaks = [l.strip() for l in (p.stdout + p.stderr).splitlines() if "LEAK" in l]
    (out / "mutation_rls.json").write_text(json.dumps({
        "mutation": "tenant_isolation policy replaced by USING (true) WITH CHECK (true)",
        "suite_failed_as_required": p.returncode != 0, "leak_reports": len(leaks), "sample": leaks[:5],
        "migration_restored_byte_identical": restored}, indent=2), encoding="utf-8")
    crit("G1.5_mutation", p.returncode != 0 and len(leaks) >= 15 and restored,
         {"suite_failed": p.returncode != 0, "leak_reports": len(leaks), "restored": restored})


# ---------------------------------------------------------------------------------------------- static
def static_checks(out: pathlib.Path) -> None:
    res: dict = {}
    res["gofmt_unformatted"] = [l for l in run(["gofmt", "-l", "."]).stdout.splitlines() if l.strip()]
    res["go_vet_ok"] = run(["go", "vet", "-tags", "integration stack", "./..."]).returncode == 0
    sc = run(["staticcheck", "-tags", "integration stack", "./..."])
    res["staticcheck_ok"] = sc.returncode == 0
    res["staticcheck_output"] = sc.stdout.strip()[:800]
    if shutil.which("gitleaks"):
        gl = run(["gitleaks", "detect", "--no-banner", "--redact"])
    else:  # CI: use the pinned container image instead of a local binary
        gl = run(["docker", "run", "--rm", "-v", f"{ROOT}:/repo", "zricethezav/gitleaks:v8.30.1", "detect",
                  "--source", "/repo", "--no-banner", "--redact"])
    res["gitleaks_ok"] = gl.returncode == 0
    sg = run(["docker", "run", "--rm", "-v", f"{ROOT}:/src", "-w", "/src", "semgrep/semgrep:latest", "semgrep", "scan",
              "--config", "p/golang", "--config", "p/secrets", "--config", "p/dockerfile", "--metrics=off", "--quiet",
              "--json", "--exclude", "gen", "--exclude", "evidence", "--exclude", "tmp", "--exclude", "bin"], timeout=900)
    try:
        findings = json.loads(sg.stdout).get("results", [])
    except json.JSONDecodeError:
        findings = [{"error": sg.stdout[:300] + sg.stderr[-300:]}]
    res["semgrep_findings"] = len(findings)
    res["semgrep_waivers"] = [
        {"rule": "go.lang.security.audit.crypto.math_random.math-random-used", "file": "internal/seedgen/world.go",
         "rationale": "seeded PRNG for deterministic synthetic data; not security-sensitive"}]
    (out / "static.json").write_text(json.dumps(res, indent=2), encoding="utf-8")
    crit("G1.11c_static", not res["gofmt_unformatted"] and res["go_vet_ok"] and res["staticcheck_ok"] and res["gitleaks_ok"]
         and res["semgrep_findings"] == 0, {k: res[k] for k in res if k != "staticcheck_output"})


def main() -> int:
    run([sys.executable, str(ROOT / "tools/dev/gen_env.py")])
    sha, dirty = git_sha()
    out = ROOT / "evidence/G1" / f"{dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')}-{sha}"
    out.mkdir(parents=True, exist_ok=True)
    log(f"G1 evidence directory: {out.relative_to(ROOT)} (git {sha}, dirty={dirty})")

    # the stack must be up for Vault and Keycloak
    up = run(["docker", "compose", "-f", "deploy/compose/docker-compose.yml", "--env-file", ".env",
              "up", "-d", "--wait", "--wait-timeout", "300"], timeout=420)
    if up.returncode != 0:
        crit("stack", False, up.stderr[-400:])
    # the stack tests of later milestones (ingest gateway) need the migrated + seeded database, the
    # service-role passwords, the Vault CA and the Kafka topics: exactly what `make up && make seed` does
    for step in (["go", "run", "./cmd/vsdb", "migrate-up"], ["go", "run", "./cmd/vsdb", "bootstrap-roles"],
                 ["go", "run", "./cmd/vspki", "bootstrap"], ["go", "run", "./cmd/vstopics"],
                 ["go", "run", "./cmd/vsdb", "seed", "-reset"]):
        r = run(step, timeout=900)
        if r.returncode != 0:
            crit("stack_prepare", False, {"step": " ".join(step), "stderr": r.stderr[-400:]})
            break
    versions = {n: (run(c).stdout or run(c).stderr).strip().splitlines()[0] for n, c in
                {"go": ["go", "version"], "buf": ["buf", "--version"], "docker": ["docker", "--version"],
                 "staticcheck": ["staticcheck", "-version"]}.items()}
    (out / "env.json").write_text(json.dumps({"utc": dt.datetime.now(dt.timezone.utc).isoformat(), "git": {"sha": sha, "dirty": dirty},
                                             "host": platform.platform(), "cpu_logical": os.cpu_count(), "versions": versions}, indent=2), encoding="utf-8")

    check_proto(out)
    run_go_tests(out)
    mutation_check(out)
    static_checks(out)

    # Regression: re-run G0. On a CI runner the host/toolchain criteria (G0.1-G0.3: 12+ CPUs, k6/kind/helm
    # installed...) describe the developer machine, not the runner, so CI runs the stack-health criteria
    # only (--stack-only); a local run executes all of G0.
    g0_cmd = [sys.executable, "tools/gates/g0.py", "--no-evidence"] + (["--stack-only"] if os.environ.get("CI") else [])
    reg = run(g0_cmd, timeout=900)
    failing = [l.split(" - ")[0] for l in reg.stdout.splitlines() if ": FAIL" in l]
    crit("G1.12a_g0_regression", reg.returncode == 0,
         {"command": " ".join(g0_cmd[1:]), "exit_code": reg.returncode, "failing_g0_criteria": failing})

    overall = "PASS" if all(c["status"] == "PASS" for c in CRIT.values()) else "FAIL"
    results = {"gate": "G1", "status_local": overall, "git": {"sha": sha, "dirty": dirty}, "criteria": CRIT,
               "pending": ["G1.12b: GitHub CI green on the milestone commit (verified after push)"]}
    (out / "results.json").write_text(json.dumps(results, indent=2, default=str), encoding="utf-8")
    (out / "run.log").write_text("\n".join(LOG), encoding="utf-8")
    log(f"G1 local result: {overall}  ->  {out.relative_to(ROOT)}")
    return 0 if overall == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
