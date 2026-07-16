.PHONY: build install clean check serve lint dev preflight

BINARY=dozor
INSTALL_PATH=$(HOME)/.local/bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

build:
	GOWORK=off go build $(LDFLAGS) -o $(BINARY) ./cmd/dozor

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
	rm -f $(BINARY)

check: build
	./$(BINARY) check

serve: build
	./$(BINARY) serve

dev:
	air
