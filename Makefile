# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOFMT=$(GOCMD) fmt

.PHONY: all build test test-integration test-short clean fmt certificate

all: build

build:
	$(GOBUILD) -o bin/zipreport-server cmd/zipreport-server/main.go
	$(GOBUILD) -o bin/browser-update cmd/browser-update/main.go

test:
	$(GOTEST) -v ./test/...

test-integration:
	$(GOTEST) -v -timeout=5m ./test/...

test-short:
	$(GOTEST) -v -short ./test/...

clean:
	$(GOCLEAN)
	rm bin/*

fmt:
	$(GOFMT) ./...

certificate:
	openssl req -x509 -nodes -newkey rsa:4096 -keyout cert/server.key -out cert/server.crt -days 3650 \
	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"
