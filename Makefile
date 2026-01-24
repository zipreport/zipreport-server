# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOFMT=$(GOCMD) fmt

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags="-s -w -X main.Version=$(VERSION)"

.PHONY: all build test test-integration test-short test-fixtures clean fmt lint docker certificate

all: build

build:
	$(GOBUILD) $(LDFLAGS) -o bin/zipreport-server ./cmd/zipreport-server/main.go
	$(GOBUILD) -ldflags="-s -w" -o bin/browser-update ./cmd/browser-update/main.go

test:
	$(GOTEST) -v -p 1 ./test/...

test-integration:
	$(GOTEST) -v -p 1 -timeout=10m ./test/...

test-short:
	$(GOTEST) -v -short -p 1 ./test/...

test-fixtures:
	cd test && ./generate_fixtures.sh

clean:
	$(GOCLEAN)
	rm -f bin/*

fmt:
	$(GOFMT) ./...

lint:
	golangci-lint run ./...
	govulncheck ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t zipreport-server:$(VERSION) .

certificate:
	openssl req -x509 -nodes -newkey rsa:4096 -keyout cert/server.key -out cert/server.crt -days 3650 \
	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"
