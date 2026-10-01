"""Generate a local .env with random credentials (never committed, never fixed in the repo)."""
import pathlib
import secrets
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
ENV = ROOT / ".env"

KEYS = [
    "POSTGRES_PASSWORD",
    "CLICKHOUSE_PASSWORD",
    "REDIS_PASSWORD",
    "S3_SECRET_KEY",
    "KEYCLOAK_ADMIN_PASSWORD",
    "VAULT_DEV_TOKEN",
    "GRAFANA_ADMIN_PASSWORD",
    "DB_APP_PASSWORD",
    "DB_GATEWAY_PASSWORD",
    "DB_BATCH_PASSWORD",
    "DB_ALERTS_PASSWORD",
    "DB_PRIVACY_PASSWORD",
    "DB_SEALER_PASSWORD",
    "PII_KEY",
    "DEMO_USER_PASSWORD",
    "KC_TEST_CLIENT_SECRET",
]


def main() -> int:
    """Create .env, or append only the keys an older .env lacks (existing values are never rewritten)."""
    existing: dict[str, str] = {}
    if ENV.exists():
        for line in ENV.read_text(encoding="utf-8").splitlines():
            if "=" in line:
                k, v = line.split("=", 1)
                existing[k] = v
    new = [f"{k}={secrets.token_urlsafe(24)}" for k in KEYS if k not in existing]
    if "S3_ACCESS_KEY" not in existing:
        new.append(f"S3_ACCESS_KEY=vs{secrets.token_hex(8)}")
    if not new:
        print(f"{ENV} is complete; leaving it untouched")
        return 0
    with ENV.open("a", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(new) + "\n")
    print(f"{ENV}: added {len(new)} generated value(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
