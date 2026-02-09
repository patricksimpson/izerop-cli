BINARY := izerop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
PREFIX := /usr/local

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build install uninstall clean test release

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

update:
	@echo "Updating izerop-cli..."
	@git pull
	@$(MAKE) install

release: clean
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		output="dist/$(BINARY)-$${os}-$${arch}"; \
		if [ "$$os" = "windows" ]; then output="$${output}.exe"; fi; \
		echo "Building $$output..."; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $$output ./cmd/izerop; \
	done
	@echo "✅ Release binaries in dist/"

clean:
	rm -rf bin/ dist/

test:
	go test ./...
