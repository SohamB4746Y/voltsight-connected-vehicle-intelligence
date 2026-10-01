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
]


def main() -> int:
    if ENV.exists():
        print(f"{ENV} already exists; leaving it untouched")
        return 0
    lines = [f"{k}={secrets.token_urlsafe(24)}" for k in KEYS]
    lines.append(f"S3_ACCESS_KEY=vs{secrets.token_hex(8)}")
    ENV.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"wrote {ENV} with {len(lines)} generated values")
    return 0


if __name__ == "__main__":
    sys.exit(main())
