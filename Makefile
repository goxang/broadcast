# Everything CI runs has a target here, so a red build can be reproduced
# locally with one command instead of by reading a workflow file.

SHELL := /usr/bin/env bash
GO ?= go

IMG          ?= ghcr.io/goxang/broadcast:latest
TARGET_IMG   ?= ghcr.io/goxang/broadcast-target:latest

BIN          := bin/broadcast

GOLANGCI_LINT_VERSION ?= v2.13.2
COVERAGE_THRESHOLD ?= 35
BENCH_COUNT ?= 6

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_.-]+:.*## / { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the controller binary
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/broadcast

.PHONY: test
test: ## Run the tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the tests with the race detector
	$(GO) test -race -shuffle=on -count=1 ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format the tree
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted
	@out=$$(gofmt -l .); \
	if [[ -n "$$out" ]]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

.PHONY: tidy-check
tidy-check: ## Fail if go.mod or go.sum would change
	@cp go.mod go.mod.bak; cp go.sum go.sum.bak; \
	$(GO) mod tidy; \
	status=0; \
	if ! diff -q go.mod go.mod.bak >/dev/null; then echo "go mod tidy changed go.mod; commit the result"; status=1; fi; \
	if ! diff -q go.sum go.sum.bak >/dev/null; then echo "go mod tidy changed go.sum; commit the result"; status=1; fi; \
	mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
	exit $$status

.PHONY: lint-deps
lint-deps: ## Install the pinned golangci-lint
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: lint
lint: ## Run golangci-lint (install it with `make lint-deps`)
	golangci-lint run

.PHONY: go.test.coverage
go.test.coverage: ## Run tests with coverage and enforce the threshold
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1
	@$(GO) tool cover -func=coverage.out | awk '/^total:/ { sub(/%/, "", $$3); \
		if ($$3 + 0 < $(COVERAGE_THRESHOLD)) { printf "coverage %.1f%% below $(COVERAGE_THRESHOLD)%% gate\n", $$3; exit 1 } }'

.PHONY: go-benchmark
go-benchmark: ## Run the benchmarks
	$(GO) test -run='^$$' -bench=. -benchmem -count=$(BENCH_COUNT) ./...

.PHONY: go-benchmark-compare
go-benchmark-compare: ## Compare benchmarks against BASE_REF (default origin/main)
	./tools/hack/go-benchmark-compare.sh

.PHONY: verify
verify: tidy-check fmt-check vet test-race lint ## Run every static check CI runs
	@echo "verify: ok"

.PHONY: docker-build
docker-build: ## Build the controller and test-target images
	docker build -t $(IMG) -f Dockerfile .
	docker build -t $(TARGET_IMG) -f test/targets/Dockerfile .

.PHONY: helm-lint
helm-lint: ## Lint and render the chart
	helm lint charts/broadcast
	helm template test charts/broadcast --namespace goxang-broadcast-system >/dev/null && echo "helm template: ok"

.PHONY: e2e
e2e: ## Run the end-to-end suite against a throwaway kind cluster
	bash test/e2e/run.sh

.PHONY: clean
clean: ## Remove build and test artifacts
	rm -f coverage.out
	rm -rf bin
	$(GO) clean -testcache
