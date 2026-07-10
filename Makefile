SHELL := /usr/bin/env bash
GO := go
NPM := npm
VERSION ?= 0.0.0-dev

.PHONY: build build-embed dev test lint tidy release clean web-install web-build stage-web size-check install-hooks coverage release-matrix dep-cycle-check

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

# M4 hard gates.

# Fail if any non-test file under internal/ crosses the 800-line
# per-file limit. Called in CI + by the pre-commit hook.
size-check:
	@./scripts/check-file-size.sh --tree

# Wire the pre-commit hook into the local .git/hooks. Idempotent.
install-hooks:
	@mkdir -p .git/hooks
	@ln -sfn ../../scripts/pre-commit .git/hooks/pre-commit
	@echo "pre-commit hook linked to scripts/pre-commit"

# Emit per-package coverage; used by CI to compare against thresholds.
coverage:
	$(GO) test -race -coverprofile=cover.out ./internal/... 2>&1 | tee coverage.log
	@$(GO) tool cover -func=cover.out | tail -1

# Local mirror of .github/workflows/release.yml's cross-compile matrix
# for cmd/5gpn-installer + cmd/5gpn-ctl — pure Go, no CGO toolchain
# needed. The daemon (CGO+embed) is not cross-compiled here; that step
# runs in Linux CI where the C toolchain is available.
release-matrix:
	@rm -rf dist/matrix && mkdir -p dist/matrix
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	  os="$${target%/*}"; arch="$${target#*/}"; \
	  echo "== $${target} =="; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build \
	    -ldflags="-s -w -X main.version=$(VERSION)" \
	    -o dist/matrix/5gpn-installer-$$os-$$arch ./cmd/5gpn-installer; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build \
	    -ldflags="-s -w -X main.version=$(VERSION)" \
	    -o dist/matrix/5gpn-ctl-$$os-$$arch ./cmd/5gpn-ctl; \
	done
	@cd dist/matrix && shasum -a 256 ./* | sed 's|\./||' > SHA256SUMS
	@ls -lh dist/matrix/
	@echo && echo "SHA256SUMS:" && cat dist/matrix/SHA256SUMS

# S1 dep-cycle guard: assert internal/rules never imports internal/config.
# Enforces the unidirectional config→rules dependency declared in the plan.
dep-cycle-check:
	@echo "Checking config→rules import direction..."
	@if go list -deps github.com/Xiuyixx/5GPN-Go/internal/rules 2>/dev/null \
	    | grep -q 'github.com/Xiuyixx/5GPN-Go/internal/config'; then \
	  echo "FAIL: internal/rules imports internal/config (cycle detected)"; exit 1; \
	fi
	@echo "OK: internal/rules does not import internal/config"
