SHELL := /bin/bash
export GOTOOLCHAIN ?= auto

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/stackorder/stackorder/internal/version.Version=$(VERSION) \
	-X github.com/stackorder/stackorder/internal/version.Commit=$(COMMIT) \
	-X github.com/stackorder/stackorder/internal/version.Date=$(DATE)

GOLANGCI_LINT_VERSION ?= v2.14.0
E2E_TIMEOUT ?= 40m

.PHONY: all build build-cli build-server test test-integration test-e2e sync-example lint fmt vet ui ui-test docs docs-dev dev down docker clean tidy

all: build

build: build-cli build-server

build-cli:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/stackorder ./cmd/stackorder

build-server:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/stackorder-server ./cmd/stackorder-server

test:
	go test -race -count=1 -coverprofile=coverage.out ./...

test-integration:
	go test -race -count=1 -tags integration -timeout 20m ./...

test-e2e:
	go test -count=1 -tags e2e -timeout $(E2E_TIMEOUT) ./test/e2e/...

EXAMPLE_INFRA ?= ../example-infra

sync-example:
	rm -rf test/integration/testdata/example-infra
	mkdir -p test/integration/testdata/example-infra
	git -C $(EXAMPLE_INFRA) ls-files -z | grep -zv '^\.github/' | rsync -a --files-from=- --from0 $(EXAMPLE_INFRA)/ test/integration/testdata/example-infra/

lint: fmt vet
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

fmt:
	@out=$$(gofmt -l . 2>/dev/null | grep -v '^ui/' | grep -v '^docs/' || true); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

tidy:
	go mod tidy

ui:
	cd ui && npm ci && npm run build

ui-test:
	cd ui && npm ci && npm test

docs:
	cd docs && npm ci && npm run build

docs-dev:
	cd docs && npm ci && npm run dev

dev:
	docker compose up -d postgres localstack
	DATABASE_URL=postgres://stackorder:stackorder@localhost:5432/stackorder?sslmode=disable \
	STACKORDER_BASE_URL=http://localhost:8080 \
	go run ./cmd/stackorder-server

down:
	docker compose down -v

docker:
	docker build -t ghcr.io/stackorder/stackorder:$(VERSION) .

clean:
	rm -rf bin dist coverage.out internal/ui/dist/* ui/dist docs/.vitepress/dist
