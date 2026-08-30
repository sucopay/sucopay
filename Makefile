.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: dev
dev: ## Run the development stack (for working on suco Pay itself)
	docker compose up

.PHONY: build
build: ## Build suco
	go build -o bin/suco ./cmd/suco

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

.PHONY: migrate
migrate: ## Apply database migrations
	go run ./cmd/suco migrate

.PHONY: clean
clean:
	rm -rf bin dist out
