VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test lint clean test-aws e2e-policy e2e-policy-diff

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

# E2E のデプロイロールへ、リポジトリのポリシーを適用する。
# iam-policy-drift はファイル同士しか見ないので、生成器を変えたらこれを回す
# (忘れると e2e-aws-* が 6 本まとめて AccessDenied で落ちる、#243)。
e2e-policy:
	./scripts/apply-e2e-policy.sh

e2e-policy-diff:
	./scripts/apply-e2e-policy.sh --dry-run

clean:
	rm -rf bin/
