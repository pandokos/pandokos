SHELL := /bin/sh
RELEASE_GO := $(shell cat .go-version)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
IMAGE ?= pandokos:dev
PKG := github.com/pandokos/pandokos
LDFLAGS := -s -w -buildid= -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME ?= 30s

.PHONY: help build test test-sdk test-container fuzz bench fmt fmt-check vet lint tidy vuln image check clean

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

build: ## Build bin/pandokos
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/pandokos ./cmd/pandokos

test: ## Unit, integration, end-to-end and repository tests, with the race detector
	go test -race -count=1 ./...

test-sdk: ## Official SDK suite (nested module, release toolchain)
	cd test/sdk && GOTOOLCHAIN=go$(RELEASE_GO) go test -race -count=1 ./...

test-container: ## Tests against a built image: make image first
	PANDOKOS_TEST_IMAGE=$(IMAGE) go test -tags container -count=1 ./test/container/

fuzz: ## Run each fuzz target for FUZZTIME
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzPrepareRequest$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzReadUsage$$' -fuzztime $(FUZZTIME)
	go test ./internal/family/openai/ -run '^$$' -fuzz '^FuzzStreamReader$$' -fuzztime $(FUZZTIME)

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

check: fmt-check vet lint tidy test test-sdk vuln ## Everything a pull request must pass, except the image

clean: ## Remove build output
	rm -rf bin dist
