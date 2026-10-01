SHELL := bash
COMPOSE := docker compose -f deploy/compose/docker-compose.yml --env-file .env
PY ?= python
GO ?= go

.PHONY: env up bootstrap gateway simulate seed verify-seed down nuke ps logs health proto er realm lint secrets-scan \
        gate-G0 gate-G0-clean gate-G1 test test-integration

env:  ## generate .env with random credentials (idempotent, appends only missing keys)
	$(PY) tools/dev/gen_env.py

up: env  ## start the stack, wait until healthy, migrate the database, set role passwords, set up the device CA
	$(COMPOSE) up -d --wait --wait-timeout 300
	$(MAKE) bootstrap

bootstrap:  ## database schema + service-role passwords + Vault PKI + Kafka topics (idempotent)
	$(GO) run ./cmd/vsdb migrate-up
	$(GO) run ./cmd/vsdb bootstrap-roles
	$(GO) run ./cmd/vspki bootstrap
	$(GO) run ./cmd/vstopics

gateway:  ## run the ingest gateway (mTLS :8443, admin :9100)
	$(GO) run ./cmd/vsgateway

simulate:  ## 60 s of the 100K fleet through the gateway (start `make gateway` first)
	$(GO) run ./cmd/vssim -vehicles 100000 -duration 60 -start-tod 27000 -gateway https://127.0.0.1:8443

seed:  ## load the deterministic 100,000-vehicle dataset and verify it against db/seed/manifest.json
	$(GO) run ./cmd/vsdb seed -reset
	$(MAKE) verify-seed

verify-seed:
	$(GO) run ./cmd/vsdb verify -manifest db/seed/manifest.json

down:
	$(COMPOSE) down

nuke:  ## remove containers AND volumes
	$(COMPOSE) down -v --remove-orphans

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100 $(SVC)

health: ps

proto:  ## lint, format and regenerate the protobuf contracts
	cd proto && buf lint && buf format -w && buf generate

er:  ## regenerate docs/er from the migrated database
	$(GO) run ./cmd/vser

realm:  ## regenerate the Keycloak realm file from the seed identities
	$(GO) run ./cmd/vsrealm

lint:  ## static checks that run without the stack
	$(COMPOSE) config -q
	yamllint -s -c .yamllint.yml deploy .github
	gofmt -l . | (! grep .)
	$(GO) vet -tags "integration stack" ./...
	staticcheck -tags "integration stack" ./...

secrets-scan:
	gitleaks detect --no-banner --redact -v

test:  ## unit tests (no Docker needed)
	$(GO) test -race -count=1 ./...

test-integration:  ## adds Testcontainers (PostgreSQL) and the running Vault/Keycloak
	$(GO) test -tags "integration stack" -count=1 -p 1 ./...

gate-G0: env  ## re-run G0 against the running stack and write evidence
	$(PY) tools/gates/g0.py

gate-G0-clean: env  ## G0 from clean volumes (cold start timing)
	$(PY) tools/gates/g0.py --clean

gate-G1: env  ## full G1 evidence run (contracts, DB, RLS, seed, Keycloak, Vault, static, coverage, mutation)
	$(PY) tools/gates/g1.py
