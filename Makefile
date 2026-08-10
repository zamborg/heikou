SHELL := /bin/sh

PREFIX ?= $(HOME)/.local
BIN_DIR ?= $(PREFIX)/bin
GO ?= go

.PHONY: build install test fmt vet check clean

build:
	mkdir -p bin
	$(GO) build -o bin/h ./cmd/h

install:
	mkdir -p "$(BIN_DIR)"
	$(GO) build -o "$(BIN_DIR)/heikou" ./cmd/h
	ln -sf heikou "$(BIN_DIR)/h"
	ln -sf heikou "$(BIN_DIR)/H"

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

check: test vet

clean:
	rm -f bin/h
