.PHONY: build install clean check serve lint dev

BINARY=dozor
INSTALL_PATH=$(HOME)/.local/bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/dozor

install: build
	mkdir -p $(INSTALL_PATH)
	cp $(BINARY) $(INSTALL_PATH)/$(BINARY)

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)

check: build
	./$(BINARY) check

serve: build
	./$(BINARY) serve

dev:
	air
