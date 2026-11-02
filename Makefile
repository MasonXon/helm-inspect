BINARY    := helm-verify
DIST      := dist
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.buildVersion=$(VERSION)
UPX       ?= upx
UPX_FLAGS ?= --best --lzma

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build build-all test vet fmt clean help

all: build

## build: compile for the host and compress with upx
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/helm-verify
	@if [ "$$(go env GOOS)" = "darwin" ]; then \
		echo "upx: skipped, packed Mach-O binaries are killed by macOS"; \
	else \
		$(UPX) $(UPX_FLAGS) $(BINARY); \
	fi

## build-all: cross-compile every target and compress each with upx
build-all:
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)-$$os-$$arch$$ext"; \
		echo ">> $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/helm-verify || exit 1; \
		if [ "$$os" = "darwin" ]; then \
			echo "   upx: skipped, packed Mach-O binaries are killed by macOS"; \
		else \
			$(UPX) $(UPX_FLAGS) "$$out" || exit 1; \
		fi; \
	done

## test: run the test suite
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format the source
fmt:
	gofmt -w *.go

## clean: remove build artifacts
clean:
	rm -rf $(BINARY) $(BINARY).exe $(DIST)

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
