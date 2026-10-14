.DEFAULT_GOAL := help
GO ?= go
BIN := bin/ledgerd

.PHONY: help build test test-unit test-race lint fmt run bench tidy generate check-generated check-docs env up down logs smoke image images smoke-images

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

# Pinned k6 image (tag and digest). The benchmark runs inside the compose network,
# so it talks to the API container directly.
K6_IMAGE := grafana/k6:2.3.0@sha256:e66db15b860113878fa74670e31f5e274830b7b6e42c8bff28b2f2d86a257603

bench: ## Load test the running compose stack (VUS, DURATION, PAIRS, SCENARIOS tune it)
	@test -f .env || { echo "no .env: run 'make up' first"; exit 1; }
	@mkdir -p bench/results
	@set -a; . ./.env; set +a; \
	  ./scripts/bench-env.sh | tee bench/results/environment.txt; \
	  docker run --rm --user "$$(id -u):$$(id -g)" --network ledgerd_default \
	    -v "$$PWD/bench/k6:/scripts:ro" -v "$$PWD/bench/results:/results" \
	    -e BASE_URL=http://api:8085 -e API_KEY="$$LEDGERD_API_KEY" \
	    -e VUS -e DURATION -e PAIRS -e SCENARIOS \
	    $(K6_IMAGE) run /scripts/transfers.js

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

check-docs: ## Fail if docs/traceability.md names a test that does not exist
	./scripts/check-traceability.sh

# ---- release images ----
# IMAGE_TAG is the version for a release build (the release workflow sets it).
IMAGE_TAG ?= dev
LEDGERD_IMAGE ?= ghcr.io/cmdevrg/ledgerd:$(IMAGE_TAG)
LEDGERD_POSTGRES_IMAGE ?= ghcr.io/cmdevrg/ledgerd-postgres:$(IMAGE_TAG)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

images: ## Build the two release images (service and PostgreSQL), tagged for IMAGE_TAG
	docker build --build-arg VERSION=$(IMAGE_TAG) --build-arg COMMIT=$(COMMIT) -t $(LEDGERD_IMAGE) .
	docker build -f deploy/postgres.Dockerfile --build-arg VERSION=$(IMAGE_TAG) --build-arg COMMIT=$(COMMIT) -t $(LEDGERD_POSTGRES_IMAGE) .

smoke-images: images env ## Start the stack from the release images, run the smoke test, remove the stack
	@set -e; export LEDGERD_IMAGE=$(LEDGERD_IMAGE) LEDGERD_POSTGRES_IMAGE=$(LEDGERD_POSTGRES_IMAGE); \
	  compose="docker compose -f docker-compose.yml -f docker-compose.images.yml"; \
	  trap '$$compose down -v' EXIT; \
	  $$compose up --wait --detach; \
	  ./scripts/smoke.sh
