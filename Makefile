BINARY := izerop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"
PREFIX := /usr/local

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
GOBIN := $(shell go env GOPATH)/bin
WAILS := $(shell command -v wails 2>/dev/null || echo "$(GOBIN)/wails")

.PHONY: build install uninstall clean test release desktop

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

desktop:
	cd cmd/desktop && $(WAILS) build -ldflags "-s -w -X main.version=$(VERSION)"
	@echo "✅ Desktop app built: cmd/desktop/build/bin/"

desktop-dev:
	cd cmd/desktop && $(WAILS) dev

desktop-update:
	@echo "⬇️  Pulling latest..."
	@git pull
	@echo "🔨 Building desktop app..."
	@cd cmd/desktop && $(WAILS) build -ldflags "-s -w -X main.version=$(VERSION)"
	@echo "✅ Updated! Run: ./cmd/desktop/build/bin/izerop"

clean:
	rm -rf bin/ dist/ cmd/desktop/build/

test:
	go test ./...
