.DEFAULT_GOAL := help

# The database the tests run against, with the credentials docker-compose.yml
# gives it. `make dev` starts one.
export SUCO_TEST_DATABASE_URL ?= postgres://sucopay:sucopay@127.0.0.1:5432/sucopay

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: dev
dev: ## Run the database in Docker
	docker compose up

.PHONY: build
build: ## Build suco
	go build -o bin/suco ./cmd/suco

.PHONY: check
check: ## Everything CI runs
	gofmt -l . | tee /dev/stderr | (! read)
	go build ./...
	go test -race ./...
	$(MAKE) lint
	$(MAKE) docs
	$(MAKE) vuln

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: lint
lint: ## Run linters
	go vet ./...
	golangci-lint run

.PHONY: vuln
vuln: ## Check dependencies for known vulnerabilities
	go mod verify
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

.PHONY: docs
docs: ## Check the documentation against the code
	./scripts/check-docs.sh
	./scripts/check-public-only.sh

.PHONY: fmt
fmt: ## Format
	gofmt -w .

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist out
