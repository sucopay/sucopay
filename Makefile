.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: dev
dev: ## Run the database suco Pay will use
	docker compose up

.PHONY: build
build: ## Build suco
	go build -o bin/suco ./cmd/suco

.PHONY: check
check: ## Everything CI runs
	gofmt -l . | tee /dev/stderr | (! read)
	go build ./...
	go vet ./...
	go test -race ./...
	golangci-lint run ./...
	./scripts/check-docs.sh
	./scripts/check-public-only.sh

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: lint
lint: ## Run linters
	go vet ./...
	golangci-lint run

.PHONY: fmt
fmt: ## Format
	gofmt -w .


.PHONY: clean
clean:
	rm -rf bin dist out
