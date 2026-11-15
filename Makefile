.PHONY: build test lint clean install cli-inventory

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
BIN     := bin/rootfiles

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/rootfiles/

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

# cli-inventory rewrites docs/cli-inventory.md from the command tree; the test of
# the same name fails when the committed page and the tree disagree.
cli-inventory:
	ROOTFILES_UPDATE=1 go test ./internal/cli -run TestDocsCommandInventory -count=1

clean:
	rm -rf bin/

install: build
	cp $(BIN) /usr/local/bin/rootfiles
