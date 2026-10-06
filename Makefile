GOOSE = goose
APP_COMPOSE = docker compose -f docker-compose.yml -f docker-compose.app.yml
export GOOSE_DRIVER = postgres
export GOOSE_MIGRATION_DIR = apps/api/migrations
export GOOSE_DBSTRING ?= postgres://watermarker:watermarker@localhost:5432/watermarker?sslmode=disable

.PHONY: up down nuke migrate-up migrate-down migrate-status migrate-create observability-up observability-down observability-nuke
.PHONY: app-up app-down app-logs

app-up:
	$(APP_COMPOSE) up -d --build --wait

app-down:
	$(APP_COMPOSE) down

app-logs:
	$(APP_COMPOSE) logs -f api consumer worker web

up:
	docker compose up -d

down:
	docker compose down

nuke:
	docker compose down -v

migrate-up:
	$(GOOSE) up

migrate-down:
	$(GOOSE) down

migrate-status:
	$(GOOSE) status

migrate-create:
	@test -n "$(name)" || (echo "usage: make migrate-create name=..." && exit 1)
	$(GOOSE) create $(name) sql

observability-up:
	docker compose -p watermarker-observability -f observability/docker-compose.yml up -d

observability-down:
	docker compose -p watermarker-observability -f observability/docker-compose.yml down

observability-nuke:
	docker compose -p watermarker-observability -f observability/docker-compose.yml down -v

.PHONY: ecs-up ecs-down
ecs-up:
	sh scripts/ecs-local.sh up

ecs-down:
	sh scripts/ecs-local.sh down

.PHONY: query-lab
query-lab:
	python3 scripts/db/query_lab.py

.PHONY: quota-query-lab
quota-query-lab:
	python3 scripts/db/query_lab.py quota-usage

.PHONY: cleanup-query-lab
cleanup-query-lab:
	python3 scripts/db/query_lab.py cleanup-scheduling

.PHONY: quota-lock-lab
quota-lock-lab:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/db/query_lab.py quota-locks
