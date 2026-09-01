.DEFAULT_GOAL := help

# The database the tests run against, with the credentials docker-compose.yml
# gives it. `make dev` starts one.
export SUCO_TEST_DATABASE_URL ?= postgres://sucopay:sucopay@127.0.0.1:5432/sucopay

# The parsers that read something somebody else wrote. Fuzzing runs their seed
# corpus as ordinary tests; `make fuzz` is the longer search.
FUZZ = ./internal/config:FuzzDecode ./internal/config:FuzzResolve \
       ./internal/invisible:FuzzQuote \
       ./internal/payment:FuzzParseMoney ./internal/payment:FuzzMetadata \
       ./internal/payment:FuzzParseAddress
FUZZTIME ?= 60s

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
	$(MAKE) repo
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

.PHONY: fuzz
fuzz: ## Fuzz each parser for a minute
	@for t in $(FUZZ); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		echo "== $$name in $$pkg"; \
		go test $$pkg -run '^$$' -fuzz "^$$name$$" -fuzztime=$(FUZZTIME) || exit 1; \
	done

.PHONY: repo
repo: ## Check the repository against its own rules
	./scripts/check-docs.sh
	./scripts/check-public-only.sh
	./scripts/check-source.sh

.PHONY: fmt
fmt: ## Format
	gofmt -w .

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist out
