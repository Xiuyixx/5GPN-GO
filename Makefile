SHELL := /usr/bin/env bash
GO := go
NPM := npm
VERSION ?= 0.0.0-dev

.PHONY: build build-embed dev test lint tidy release clean web-install web-build stage-web

build:
	$(GO) build ./cmd/5gpn ./cmd/5gpn-installer ./cmd/5gpn-ctl

# Copies web/dist into internal/web/dist so //go:embed can find it, then
# builds the daemon with the embed tag. The staged dist is not committed
# (internal/web/dist is ignored).
stage-web:
	@rm -rf internal/web/dist
	@cp -R web/dist internal/web/dist

build-embed: stage-web
	$(GO) build -tags embed -o dist/5gpn ./cmd/5gpn

dev:
	@echo "M0: run \`cd web && npm run dev\` and \`go run ./cmd/5gpn\` in separate shells."

test:
	$(GO) test -race ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"
	@cd web && $(NPM) run lint || echo "web lint failed"

tidy:
	$(GO) mod tidy

web-install:
	cd web && $(NPM) ci --no-audit --no-fund

web-build:
	cd web && $(NPM) run build

release: web-install web-build stage-web
	CGO_ENABLED=1 $(GO) build -tags 'embed osusergo netgo' -ldflags='-s -w -X main.version=$(VERSION)' -o dist/5gpn ./cmd/5gpn
	CGO_ENABLED=1 $(GO) build -ldflags='-s -w -X main.version=$(VERSION)' -o dist/5gpn-installer ./cmd/5gpn-installer
	CGO_ENABLED=0 $(GO) build -ldflags='-s -w -X main.version=$(VERSION)' -o dist/5gpn-ctl ./cmd/5gpn-ctl
	@ls -lh dist/

clean:
	rm -rf dist/ web/dist/ internal/web/dist cover.out coverage.out
