SHELL := /bin/sh

PREFIX ?= $(HOME)/.local
BIN_DIR ?= $(PREFIX)/bin
GO ?= go
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@2025.1.1

.PHONY: build install test race fmt fmt-check tidy-check vet staticcheck check clean

build:
	mkdir -p bin
	$(GO) build -o bin/h ./cmd/h

install:
	mkdir -p "$(BIN_DIR)"
	$(GO) build -o "$(BIN_DIR)/heikou" ./cmd/h
	ln -sf heikou "$(BIN_DIR)/h"
	ln -sf heikou "$(BIN_DIR)/H"
	@printf '\nNext: run h doctor, then h quickstart\n'

test:
	$(GO) test ./...

# The end-to-end suite needs tmux, and skips itself without it. Requiring it
# here means `make race` cannot quietly cover less than it appears to.
race:
	HEIKOU_TEST_REQUIRE_TMUX=1 $(GO) test -race ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to run on:"; echo "$$unformatted"; exit 1; \
	fi

tidy-check:
	@$(GO) mod tidy
	@git diff --exit-code -- go.mod go.sum || \
		{ echo "go.mod or go.sum is out of date; run go mod tidy"; exit 1; }

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) run $(STATICCHECK) ./...

# The same gates CI runs, in the same order. A local `make check` that is
# weaker than CI just moves the discovery of a break to the pull request.
check: fmt-check tidy-check vet staticcheck test race build

clean:
	rm -f bin/h
