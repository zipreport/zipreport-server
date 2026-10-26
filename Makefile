# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build -trimpath -buildvcs=false
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOLANGCI_LINT_VERSION ?= 2.15.0
GOVULNCHECK_VERSION ?= 1.9.0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags="-s -w -X main.Version=$(VERSION)"

.PHONY: all build test test-integration test-short test-upstream test-fixtures clean fmt lint docker certificate

all: build

build:
	$(GOBUILD) $(LDFLAGS) -o bin/zipreport-server ./cmd/zipreport-server/main.go
	$(GOBUILD) -ldflags="-s -w" -o bin/browser-update ./cmd/browser-update/main.go

test:
	$(GOTEST) -v -p 1 ./test/... ./internal/...

test-integration:
	$(GOTEST) -v -p 1 -timeout=10m ./test/... ./internal/... ./pkg/...

test-short:
	$(GOTEST) -v -short -p 1 ./test/... ./internal/... ./pkg/...

UPSTREAM_COMPOSE = docker compose -f test/upstream/docker-compose.yml

# Upstream proxy tests against a real Squid (needs Docker + openssl); see test/upstream/docker-compose.yml
test-upstream:
	mkdir -p test/upstream/certs
	openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj "/CN=origin.test" \
	      -addext "subjectAltName=DNS:origin.test" \
	      -keyout test/upstream/certs/origin.key -out test/upstream/certs/origin.crt 2>/dev/null
	chmod 644 test/upstream/certs/origin.key
	$(UPSTREAM_COMPOSE) run --rm --build tests; rc=$$?; $(UPSTREAM_COMPOSE) --profile tests down -v; exit $$rc

test-fixtures:
	cd test && ./generate_fixtures.sh

clean:
	$(GOCLEAN)
	rm -f bin/*

fmt:
	gofmt -s -w .

lint:
	@out=$$(golangci-lint version 2>&1); \
	want=$$(sed -n 's/^go \([0-9.]*\)$$/\1/p' go.mod); \
	got=$$(printf '%s\n' "$$out" | sed -n 's/.* built with go\([0-9.]*\).*/\1/p'); \
	if ! printf '%s\n' "$$out" | grep -q "version $(GOLANGCI_LINT_VERSION) " || [ -z "$$got" ] || ! printf '%s\n%s\n' "$$want" "$$got" | sort -V -C; then \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) built with go >= $$want required; found: $$out"; \
		echo "install: GOTOOLCHAIN=go$$want go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)"; \
		exit 1; \
	fi
	@if ! govulncheck -version 2>&1 | grep -q "@v$(GOVULNCHECK_VERSION)"; then \
		echo "govulncheck $(GOVULNCHECK_VERSION) required"; \
		echo "install: GOTOOLCHAIN=go$$(sed -n 's/^go \([0-9.]*\)$$/\1/p' go.mod) go install golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION)"; \
		exit 1; \
	fi
	@echo "lint tools OK: golangci-lint $(GOLANGCI_LINT_VERSION), govulncheck $(GOVULNCHECK_VERSION)"
	golangci-lint run ./...
	govulncheck ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t zipreport-server:$(VERSION) .

certificate:
	mkdir -p cert
	openssl req -x509 -nodes -newkey rsa:4096 -keyout cert/server.key -out cert/server.crt -days 3650 \
	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"
