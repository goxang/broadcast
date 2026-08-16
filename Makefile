SHELL := /bin/bash

IMG          ?= ghcr.io/goxang/broadcast:latest
TARGET_IMG   ?= ghcr.io/goxang/broadcast-target:latest

BIN          := bin/broadcast

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/broadcast

.PHONY: test
test:
	go test ./...

.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: verify
verify: fmt vet test test-race
	@echo "verify: ok"

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) -f Dockerfile .
	docker build -t $(TARGET_IMG) -f test/targets/Dockerfile .

.PHONY: helm-lint
helm-lint:
	helm lint charts/broadcast
	helm template test charts/broadcast --namespace goxang-broadcast-system >/dev/null && echo "helm template: ok"

.PHONY: e2e
e2e:
	bash test/e2e/run.sh
