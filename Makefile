BINARY := izerop
VERSION := 0.1.0
LDFLAGS := -ldflags "-s -w"
PREFIX := /usr/local

.PHONY: build install uninstall clean test

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/izerop

install: build
	@if [ -w "$(PREFIX)/bin" ]; then \
		echo "Installing $(BINARY) to $(PREFIX)/bin..."; \
		cp bin/$(BINARY) $(PREFIX)/bin/$(BINARY); \
		chmod +x $(PREFIX)/bin/$(BINARY); \
	else \
		echo "Installing $(BINARY) to ~/.local/bin..."; \
		mkdir -p $(HOME)/.local/bin; \
		cp bin/$(BINARY) $(HOME)/.local/bin/$(BINARY); \
		chmod +x $(HOME)/.local/bin/$(BINARY); \
	fi
	@echo "✅ Installed! Run 'izerop version' to verify."

uninstall:
	@rm -f $(PREFIX)/bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "✅ Uninstalled $(BINARY)"

clean:
	rm -rf bin/

test:
	go test ./...
