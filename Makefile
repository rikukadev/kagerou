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

# moto server に対する AWS 結合テスト(DESIGN.md §9)。要 docker。
# ホスト側は 5001(macOS は AirPlay Receiver が 5000 を掴んでいるため)。
test-aws:
	@docker start kagerou-moto 2>/dev/null || docker run -d --name kagerou-moto -p 5001:5000 motoserver/moto:latest
	@sleep 1
	AWS_ENDPOINT_URL=http://127.0.0.1:5001 \
	AWS_ACCESS_KEY_ID=testing AWS_SECRET_ACCESS_KEY=testing AWS_REGION=us-east-1 \
	go test -race -count=1 ./internal/driver/...; \
	status=$$?; docker rm -f kagerou-moto >/dev/null; exit $$status

clean:
	rm -rf bin/
