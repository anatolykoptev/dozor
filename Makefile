.PHONY: build install clean check serve

BINARY=dozor
INSTALL_PATH=$(HOME)/.local/bin

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_PATH)
	cp $(BINARY) $(INSTALL_PATH)/$(BINARY)

clean:
	rm -f $(BINARY)

check: build
	./$(BINARY) check

serve: build
	./$(BINARY) serve
