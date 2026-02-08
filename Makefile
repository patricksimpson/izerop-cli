BINARY := izerop
VERSION := 0.1.0
LDFLAGS := -ldflags "-s -w"

.PHONY: build install clean test

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/izerop

install: build
	cp bin/$(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || cp bin/$(BINARY) ~/go/bin/$(BINARY)

clean:
	rm -rf bin/

test:
	go test ./...
