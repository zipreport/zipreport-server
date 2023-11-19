# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

all: build

build:
	$(GOBUILD) cmd/zipreport-server/ -o bin/zipreport-server

test:
	$(GOTEST) -v pkg/render/*
	$(GOTEST) -v pkg/storage/*
	$(GOTEST) -v pkg/zptserver/*

clean:
	$(GOCLEAN)
	rm bin/*

certificate:
	openssl req -x509 -nodes -newkey rsa:4096 -keyout cert/server.key -out cert/server.crt -days 3650 \
	      -subj "/C=PT/ST=Lisbon/L=Lisbon/O=ZipReport/OU=RD/CN=zipreport-server.local"
