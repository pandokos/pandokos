SHELL := /bin/sh
RELEASE_GO := $(shell cat .go-version)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
IMAGE ?= pandokos:dev
PKG := github.com/pandokos/pandokos
LDFLAGS := -s -w -buildid= -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
GITLEAKS := github.com/zricethezav/gitleaks/v8@v8.30.1
SQLC := github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
GENERATED := internal/adminapi internal/store/queries
FUZZTIME ?= 30s

# Loads local/testdb/env (written by make db-up) unless the variable is already set.
DBENV = set -a; if [ -z "$$PANDOKOS_TEST_DATABASE_URL" ] && [ -f local/testdb/env ]; then . ./local/testdb/env; fi; set +a;

.PHONY: help build test test-sdk test-container fuzz bench fmt fmt-check vet lint tidy vuln image check clean db-up db-down test-nodb migrations-sum generate generate-check secrets

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

build: ## Build bin/pandokos
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/pandokos ./cmd/pandokos

test: ## All tests of the root module, with the race detector (needs make db-up)
	@$(DBENV) go test -race -count=1 ./...

test-nodb: ## The same without the tests that need PostgreSQL (local use only, refused in CI)
	PANDOKOS_TEST_NO_DATABASE=1 PANDOKOS_TEST_NO_POOLER=1 PANDOKOS_TEST_NO_DATABASE_TLS=1 go test -race -count=1 ./...

test-sdk: ## Official SDK suite (nested module, release toolchain)
	cd test/sdk && GOTOOLCHAIN=go$(RELEASE_GO) go test -race -count=1 ./...

test-container: ## Tests against a built image: make image and make db-up first
	@$(DBENV) PANDOKOS_TEST_IMAGE=$(IMAGE) go test -tags container -count=1 ./test/container/

db-up: ## Start the test PostgreSQL and PgBouncer (Docker)
	sh test/db/up.sh

db-down: ## Stop them and discard the data
	docker compose -f test/db/compose.yaml down -v

migrations-sum: ## Rewrite the checksum file of the migrations
	@cd internal/store/migrations/sql && for f in *.sql; do printf '%s %s\n' "$$f" "$$(sha256sum "$$f" | cut -d' ' -f1)"; done > ../migrations.sum

generate: ## Regenerate internal/adminapi (apigen) and internal/store/queries (sqlc)
	go run ./internal/apigen -spec api/openapi/admin.v1.yaml -out internal/adminapi/adminapi.gen.go
	CGO_ENABLED=0 GOTOOLCHAIN=go$(RELEASE_GO) go run $(SQLC) generate

generate-check: generate ## Fail if the generated code is not what is committed
	@test -z "$$(git status --porcelain -- $(GENERATED))" || { git status --porcelain -- $(GENERATED); echo "generated code is not current: run make generate and commit the result"; exit 1; }

fuzz: ## Run each fuzz target for FUZZTIME
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzParseRequest$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzPrepareRequest$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzReadUsage$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzStreamReader$$' -fuzztime $(FUZZTIME)
	go test ./internal/authn/ -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME)

bench: ## Usage reader benchmark
	go test ./internal/family/openai/ -run '^$$' -bench . -benchmem

fmt: ## Format the code
	gofmt -w cmd internal test

fmt-check: ## Fail if a file is not formatted
	@test -z "$$(gofmt -l cmd internal test)" || { gofmt -l cmd internal test; exit 1; }

vet: ## go vet
	go vet ./...

lint: ## golangci-lint
	golangci-lint run

tidy: ## Check that go.mod and go.sum are tidy
	go mod tidy -diff

vuln: ## govulncheck with the release toolchain
	GOTOOLCHAIN=go$(RELEASE_GO) go run $(GOVULNCHECK) -test ./...
	cd test/sdk && GOTOOLCHAIN=go$(RELEASE_GO) go run $(GOVULNCHECK) -test ./...

image: ## Build the container image for the local platform
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

secrets: ## Scan the repository and its whole history for keys and other secrets (gitleaks)
	go run $(GITLEAKS) git --config .gitleaks.toml --redact --no-banner .

check: fmt-check vet lint tidy generate-check secrets test test-sdk vuln ## Everything a pull request must pass, except the image

clean: ## Remove build output
	rm -rf bin dist
