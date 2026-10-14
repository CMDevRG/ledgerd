.DEFAULT_GOAL := help
GO ?= go
BIN := bin/ledgerd

.PHONY: help build test test-unit test-race lint fmt run bench tidy generate check-generated env up down logs smoke image

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the binary into ./bin
	$(GO) build -trimpath -o $(BIN) ./cmd/ledgerd

# The integration tests need Docker. These targets require it, so a stopped
# Docker Desktop makes the run FAIL instead of silently skipping the database
# tests and looking green. `make test-unit` is the explicit way to skip them.
test: ## Run all tests (requires Docker)
	LEDGERD_REQUIRE_DOCKER=1 $(GO) test ./...

test-unit: ## Run the tests that need no Docker
	$(GO) test ./cmd/... ./internal/...

test-race: ## Run all tests with the race detector (requires Docker)
	LEDGERD_REQUIRE_DOCKER=1 $(GO) test -race -count=1 ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format code
	golangci-lint fmt ./...

run: ## Run the service (requires configuration in the environment)
	$(GO) run ./cmd/ledgerd serve

bench: ## Run the k6 benchmark (added in task 25)
	@echo "benchmark not implemented yet (task 25)"; exit 1

tidy: ## Tidy go.mod
	$(GO) mod tidy

generate: ## Regenerate code from SQL queries (sqlc)
	sqlc generate

check-generated: generate ## Fail if generated code is out of date
	@git diff --exit-code -- internal/store/db || (echo "generated code is stale: run 'make generate' and commit"; exit 1)

# ---- local stack (Docker Compose) ----

env: ## Create .env with random secrets (only if it does not exist)
	@if [ -f .env ]; then echo ".env already exists; leaving it untouched"; else \
	  gen() { head -c 24 /dev/urandom | base64 | tr -d '/+=\n'; }; \
	  sed -e "s|^LEDGERD_API_KEY=.*|LEDGERD_API_KEY=$$(gen)$$(gen)|" \
	      -e "s|^POSTGRES_SUPERUSER_PASSWORD=.*|POSTGRES_SUPERUSER_PASSWORD=$$(gen)|" \
	      -e "s|^LEDGERD_OWNER_PASSWORD=.*|LEDGERD_OWNER_PASSWORD=$$(gen)|" \
	      -e "s|^LEDGERD_APP_PASSWORD=.*|LEDGERD_APP_PASSWORD=$$(gen)|" \
	      .env.example > .env && chmod 600 .env && echo "created .env with random secrets"; \
	fi

up: env ## Build and start PostgreSQL, run migrations, start the API
	LEDGERD_VERSION=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
	LEDGERD_COMMIT=$$(git rev-parse HEAD 2>/dev/null || echo unknown) \
	docker compose up --build --wait --detach

down: ## Stop the stack (keeps data); use `docker compose down -v` to delete it
	docker compose down

logs: ## Follow the API logs
	docker compose logs -f api

smoke: ## End-to-end check against the running stack
	./scripts/smoke.sh

image: ## Build only the Docker image
	docker build -t ledgerd:local .
