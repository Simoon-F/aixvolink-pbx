GO ?= go
BIN_DIR ?= bin
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck
GO_LICENSES ?= go-licenses

.PHONY: all fmt fmt-check vet lint test test-race bench vulncheck license-check build build-linux-amd64 build-linux-arm64 tools clean

all: fmt-check vet lint test test-race vulncheck license-check build

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

bench:
	$(GO) test -run='^$$' -bench=. -benchmem ./spikes/diago/...

vulncheck:
	$(GOVULNCHECK) ./...

license-check:
	GO_LICENSES=$(GO_LICENSES) ./scripts/check_licenses.sh

build: build-linux-amd64 build-linux-arm64

build-linux-amd64:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN_DIR)/aixvolinkpbx-linux-amd64 ./cmd/aixvolinkpbx

build-linux-arm64:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o $(BIN_DIR)/aixvolinkpbx-linux-arm64 ./cmd/aixvolinkpbx

tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	$(GO) install golang.org/x/vuln/cmd/govulncheck@v1.5.0
	$(GO) install github.com/google/go-licenses@v1.6.0

clean:
	rm -rf $(BIN_DIR) coverage.out *.prof
