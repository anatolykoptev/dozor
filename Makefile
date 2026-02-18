.PHONY: build install clean check serve

BINARY=dozor
INSTALL_PATH=$(HOME)/.local/bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/dozor

install: build
	mkdir -p $(INSTALL_PATH)
	cp $(BINARY) $(INSTALL_PATH)/$(BINARY)

clean:
	rm -f $(BINARY)

check: build
	./$(BINARY) check

serve: build
	./$(BINARY) serve
