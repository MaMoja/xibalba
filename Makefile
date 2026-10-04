VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/MaMoja/xibalba/internal/buildinfo.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: build run test bench browser-check webserver-check lint cross check clean help

help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

build: ## Build bin/xibalba for this machine
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/xibalba ./cmd/xibalba

run: build ## Build and run with the example configuration
	./bin/xibalba -config xibalba.example.yaml

test: ## Run all tests with the race detector
	go test -race ./...

bench: ## Run the benchmarks
	go test -run '^$$' -bench . -benchmem ./...

browser-check: build ## Check the visitor pages in a real browser (needs Playwright; AXE=path/to/axe.min.js adds accessibility)
	python3 test/browser/check.py --binary bin/xibalba $(if $(AXE),--axe $(AXE))

webserver-check: build ## Run Xibalba behind real nginx, Caddy, Apache, HAProxy and Traefik with the example configurations (those installed)
	python3 test/webserver/check.py --binary bin/xibalba

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
