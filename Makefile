SHELL := /bin/bash
export GOTOOLCHAIN := local
export CGO_ENABLED := 1
ifeq ($(shell go env GOOS),darwin)
# Pin compilation AND linking. The explicit flags also invalidate cached CGO
# objects built for a newer host; MACOSX_DEPLOYMENT_TARGET alone does not.
export MACOSX_DEPLOYMENT_TARGET := 14.0
CGO_CFLAGS ?= -O2 -g
CGO_CXXFLAGS ?= -O2 -g
CGO_LDFLAGS ?= -O2 -g
export CGO_CFLAGS := $(CGO_CFLAGS) -mmacosx-version-min=14.0
export CGO_CXXFLAGS := $(CGO_CXXFLAGS) -mmacosx-version-min=14.0
export CGO_LDFLAGS := $(CGO_LDFLAGS) -mmacosx-version-min=14.0
endif
VERSION ?= dev
COMMIT ?= dev
BUILD_TIME ?= dev
# Empty locally accepts >=2.11.4,<3.0.0; CI supplies its exact installed version.
GOLANGCI_LINT_VERSION ?=
EXE := $(shell go env GOEXE)
LDFLAGS := -X rasterdock/internal/buildinfo.Version=$(VERSION) -X rasterdock/internal/buildinfo.Commit=$(COMMIT) -X rasterdock/internal/buildinfo.BuildTime=$(BUILD_TIME)
.DEFAULT_GOAL := help
.PHONY: all build run fmt fmt-check vet lint test test-integration test-coverage mod-tidy mod-check workflow-check clean help
all: fmt-check mod-check vet lint test workflow-check build
build:
	go build -mod=readonly -trimpath -ldflags '$(LDFLAGS)' -o bin/rasterdock$(EXE) ./cmd/rasterdock
ifeq ($(shell go env GOOS),darwin)
	otool -l bin/rasterdock$(EXE) | awk '$$1 == "minos" { found = 1; if ($$2 != "14.0") exit 1 } END { if (!found) exit 1 }'
endif
run:
	go run -mod=readonly ./cmd/rasterdock
fmt:
	golangci-lint fmt
fmt-check:
	golangci-lint fmt --diff --diff-colored=false
vet:
	go vet ./...
lint:
	bash scripts/check-lint-version-test.sh
	bash scripts/check-lint-version.sh "$$(golangci-lint version)" "$(GOLANGCI_LINT_VERSION)"
	golangci-lint config verify
	golangci-lint run
	golangci-lint run --build-tags=integration
test:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...
test-integration:
	go test -tags=integration -count=1 -timeout=120s -v ./...
test-coverage: test
	go tool cover -html=coverage.out -o coverage.html
mod-tidy:
	go mod tidy
mod-check:
	go mod tidy -diff
	go mod verify
workflow-check:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= .github/workflows/*.yml
clean:
	rm -rf bin dist coverage.out coverage.html
help:
	@echo 'build run fmt fmt-check vet lint test test-integration test-coverage mod-tidy mod-check workflow-check all clean'
