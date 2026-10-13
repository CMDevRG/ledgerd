.DEFAULT_GOAL := help
GO ?= go
BIN := bin/ledgerd

.PHONY: help build test test-race lint fmt run bench tidy

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the binary into ./bin
	$(GO) build -trimpath -o $(BIN) ./cmd/ledgerd

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests with the race detector
	$(GO) test -race -count=1 ./...

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
