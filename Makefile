VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test lint clean

build:
	go build $(LDFLAGS) -o bin/kagerou ./cmd/kagerou

test:
	go vet ./...
	go test -race ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
