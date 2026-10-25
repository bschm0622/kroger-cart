.PHONY: build test lint install clean

build:
	go build -o bin/kroger-pp-cli ./cmd/kroger-pp-cli

test:
	go test ./...

lint:
	golangci-lint run

install:
	go install ./cmd/kroger-pp-cli

clean:
	rm -rf bin/

build-mcp:
	go build -o bin/kroger-pp-mcp ./cmd/kroger-pp-mcp

install-mcp:
	go install ./cmd/kroger-pp-mcp

build-all: build build-mcp
