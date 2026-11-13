VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BINARY  := agents
# Keep in step with .github/workflows/ci.yml.
GOLANGCI_LINT := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

.PHONY: build test lint install clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/agents

test:
	go test -race -count=1 ./...

# Runs the same pinned golangci-lint as CI; nothing to install.
lint:
	go run $(GOLANGCI_LINT) run ./...

install: build
	install -d $(HOME)/.local/bin
	install -m 755 bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)

clean:
	rm -rf bin/ dist/
