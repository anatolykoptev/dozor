.PHONY: build build-worker check-pair install clean check serve lint dev preflight

BINARY=dozor
INSTALL_PATH=$(HOME)/.local/bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION) -X github.com/anatolykoptev/dozor/internal/agentproto.BuildID=$(VERSION)"

build:
	GOWORK=off go build $(LDFLAGS) -o $(BINARY) ./cmd/dozor

# The per-identity agent worker. dozor and the worker are both stamped with the
# same BuildID (LDFLAGS), so the client can detect a pair built from different
# revisions. (-X main.version is a no-op for the worker, which has no such var.)
build-worker:
	GOWORK=off go build $(LDFLAGS) -o dozor-worker ./cmd/dozor-worker

# Fails unless dozor and dozor-worker carry the same, non-empty
# agentproto.BuildID, i.e. they were built from one revision.
check-pair: build build-worker
	@a=$$(go version -m $(BINARY) | grep -o 'agentproto.BuildID=[^ "]*'); \
	  b=$$(go version -m dozor-worker | grep -o 'agentproto.BuildID=[^ "]*'); \
	  if [ -z "$$a" ] || [ "$$a" != "$$b" ]; then \
	    echo "FAIL: build IDs differ or are unstamped: dozor=[$$a] dozor-worker=[$$b]"; exit 1; \
	  fi; \
	  echo "ok: $$a, protocol $$(./dozor-worker --protocol)"

# Pre-merge gate (mirrors the fleet standard). No -race here: a pre-existing
# data race in TestCanaryDeploy is tracked separately; -race belongs in a
# nightly once that is fixed.
preflight:
	@echo "==> gofmt -l (vendor/ excluded — upstream code, never reformatted)"
	@dirty=$$(gofmt -l . | grep -v '^vendor/' || true); \
	  if [ -n "$$dirty" ]; then \
	    echo "FAIL: gofmt -- the following files are not formatted (run: gofmt -w <file>):"; \
	    echo "$$dirty"; \
	    exit 1; \
	  fi
	@echo "==> go vet ./..."
	GOWORK=off go vet ./...
	@echo "==> go build ./..."
	GOWORK=off go build ./...
	@echo "==> go test -count=1 ./..."
	GOWORK=off go test -count=1 ./...

install: build
	mkdir -p $(INSTALL_PATH)
	cp $(BINARY) $(INSTALL_PATH)/$(BINARY)

lint:
	GOWORK=off golangci-lint run ./...

clean:
	rm -f $(BINARY) dozor-worker

check: build
	./$(BINARY) check

serve: build
	./$(BINARY) serve

dev:
	air
