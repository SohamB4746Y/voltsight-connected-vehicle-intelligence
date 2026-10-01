SHELL := bash
COMPOSE := docker compose -f deploy/compose/docker-compose.yml --env-file .env
PY ?= python

.PHONY: env up down nuke ps logs health gate-G0 gate-G0-clean lint secrets-scan

env:  ## generate .env with random credentials (idempotent)
	$(PY) tools/dev/gen_env.py

up: env  ## start base infrastructure and wait until healthy
	$(COMPOSE) up -d --wait --wait-timeout 300

down:
	$(COMPOSE) down

nuke:  ## remove containers AND volumes
	$(COMPOSE) down -v --remove-orphans

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100 $(SVC)

health: ps

lint:  ## static checks that run without the stack
	$(COMPOSE) config -q
	yamllint -s -c .yamllint.yml deploy .github

secrets-scan:
	gitleaks detect --no-banner --redact -v

gate-G0: env  ## re-run G0 against the running stack and write evidence
	$(PY) tools/gates/g0.py

gate-G0-clean: env  ## G0 from clean volumes (cold start timing)
	$(PY) tools/gates/g0.py --clean
