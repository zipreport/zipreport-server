# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOFMT=$(GOCMD) fmt

all: build

build:
	$(GOBUILD) -o bin/zipreport-server cmd/zipreport-server/main.go
	$(GOBUILD) -o bin/browser-update cmd/browser-update/main.go

test:
	$(GOTEST) -v pkg/render/*
	$(GOTEST) -v pkg/zpt/*
	$(GOTEST) -v pkg/apiserver/*

clean:
	$(GOCLEAN)
	rm bin/*

fmt:
	$(GOFMT) ./...

certificate:
	openssl req -x509 -nodes -newkey rsa:4096 -keyout cert/server.key -out cert/server.crt -days 3650 \
	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"
