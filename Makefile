VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/MaMoja/xibalba/internal/buildinfo.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: build run test lint cross check clean help

help: ## Show this help
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

build: ## Build bin/xibalba for this machine
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/xibalba ./cmd/xibalba

run: build ## Build and run with the example configuration
	./bin/xibalba -config xibalba.example.yaml

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Check formatting and run go vet (and golangci-lint if installed)
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "not gofmt-formatted:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

cross: ## Build for linux/amd64 and linux/arm64 into dist/
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/xibalba-linux-amd64 ./cmd/xibalba
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/xibalba-linux-arm64 ./cmd/xibalba

check: lint test cross ## Everything CI runs

clean: ## Remove build output
	rm -rf bin dist
