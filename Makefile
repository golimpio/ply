# ply: common tasks. `make` (or `make help`) lists them.
#
# Needs Go, Node/npm and ffmpeg/ffprobe on PATH; the browser checks (e2e) also
# need Google Chrome. Works with the GNU Make 3.81 that ships with macOS.
#
# Examples:
#   make build                         web UI + server -> bin/ply
#   make run MEDIA=~/Movies            build, then serve that folder on :8420
#   make run ARGS="-token s3cret"      extra flags for ply
#   make dev                           server + live-reloading web UI on :5173
#   make test                          Go and web unit tests
#   make verify                        everything a change should pass

GO     ?= go
NPM    ?= npm
BIN    := bin/ply
PREFIX ?= /usr/local

# The version ply reports: the nearest tag, else the commit (-dirty when the
# tree has changes). Empty outside a git checkout, which keeps main's default.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -s -w
ifneq ($(VERSION),)
LDFLAGS += -X main.version=$(VERSION)
endif

# What `make run` and `make dev` serve: MEDIA is one folder (a leading ~ is
# expanded), more go in ARGS as further -media flags.
MEDIA ?= $(firstword $(wildcard $(HOME)/Movies $(HOME)/Videos))
MEDIA_PATH = $(patsubst ~%,$(HOME)%,$(MEDIA))
ADDR  ?= :8420
ARGS  ?=

# Release builds: `make dist` cross-compiles each of these (no cgo needed).
PLATFORMS ?= darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

WEB_SRC := $(shell find web/src web/public -type f 2>/dev/null) \
	web/index.html web/package.json web/vite.config.ts web/svelte.config.js web/tsconfig.json
WEB_DEPS := web/node_modules/.package-lock.json
WEB_DIST := web/dist/index.html

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@echo "Usage: make <target> [MEDIA=dir] [ARGS=\"-flag value\"]"
	@echo
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

# ---- build ----------------------------------------------------------------------

.PHONY: deps
deps: $(WEB_DEPS) ## Install the web UI's npm packages (Go fetches its own)

# The lock file alone: an edit to package.json's scripts must not trigger npm ci, which
# deletes node_modules first (and fails with ENOTEMPTY if anything else is using it).
$(WEB_DEPS): web/package-lock.json
	cd web && $(NPM) ci
	@touch $@

.PHONY: web
web: $(WEB_DIST) ## Build the web UI into web/dist (only when its sources changed)

$(WEB_DIST): $(WEB_DEPS) $(WEB_SRC)
	cd web && $(NPM) run build
	@touch $@

.PHONY: build
build: web ## Build the web UI and the server into bin/ply
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/ply
	@echo "built $(BIN) $(VERSION)"

.PHONY: build-go
build-go: ## Build only the server (keeps whatever web/dist holds)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/ply

.PHONY: dist
dist: web ## Cross-compile release binaries into bin/ (PLATFORMS="os/arch ...")
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; out=bin/ply-$(VERSION)-$$os-$$arch; \
		echo "$$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/ply; \
	done

# ---- run ------------------------------------------------------------------------

.PHONY: run
run: build ## Build, then serve MEDIA on ADDR (default :8420)
	@test -n "$(MEDIA_PATH)" || { echo "Set MEDIA, e.g. make run MEDIA=~/Movies"; exit 2; }
	./$(BIN) -addr $(ADDR) -media "$(MEDIA_PATH)" $(ARGS)

.PHONY: dev
dev: deps build-go ## Server on :8420 plus the web UI with live reload on :5173
	@test -n "$(MEDIA_PATH)" || { echo "Set MEDIA, e.g. make dev MEDIA=~/Movies"; exit 2; }
	@echo "Open http://localhost:5173 (Ctrl-C stops both)"
	@pid=; trap 'test -n "$$pid" && kill $$pid 2>/dev/null' EXIT INT TERM; \
		./$(BIN) -addr :8420 -media "$(MEDIA_PATH)" $(ARGS) & pid=$$!; \
		cd web && $(NPM) run dev

# ---- tests and checks -----------------------------------------------------------

.PHONY: test
test: test-go test-web ## Go and web unit tests

.PHONY: test-go
test-go: ## Go unit tests
	$(GO) test -count=1 ./...

.PHONY: test-race
test-race: ## Go unit tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: test-integration
test-integration: web ## Go integration tests (real ffmpeg, slower)
	$(GO) test -tags=integration -count=1 ./...

.PHONY: test-web
test-web: $(WEB_DEPS) ## Web unit tests (vitest)
	cd web && $(NPM) test

.PHONY: e2e
e2e: e2e-web e2e-player ## All browser checks (needs Google Chrome)

.PHONY: e2e-web
e2e-web: web ## Browser checks against a mocked API (player, metadata, TVDB, ratings, folders, activity, navigation, settings, library grid, watched)
	cd web && node e2e/player-wiring.mjs && node e2e/metadata-ui.mjs && node e2e/folders-ui.mjs && node e2e/activity-ui.mjs && node e2e/player-limits.mjs && node e2e/tvdb-ui.mjs && node e2e/ratings-ui.mjs && node e2e/navigation.mjs && node e2e/settings-ui.mjs && node e2e/library-ui.mjs && node e2e/player-ui.mjs && node e2e/watched-ui.mjs

.PHONY: e2e-player
e2e-player: build ## Browser checks of the player against a real ply (throwaway library)
	PLY_BIN=$(CURDIR)/$(BIN) sh scripts/e2e-player.sh

.PHONY: check
check: vet fmt-check check-web ## Static checks: go vet, gofmt, svelte-check

.PHONY: vet
vet:
	$(GO) vet ./...
	$(GO) vet -tags=integration ./...

.PHONY: fmt-check
fmt-check:
	@out=$$(gofmt -l cmd internal web/embed.go); \
		if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: check-web
check-web: $(WEB_DEPS)
	cd web && $(NPM) run check

.PHONY: fmt
fmt: ## Format the Go code
	gofmt -w cmd internal web/embed.go

.PHONY: lint
lint: ## golangci-lint, when it is installed
	@command -v golangci-lint >/dev/null || { echo "golangci-lint is not installed"; exit 1; }
	golangci-lint run ./...

.PHONY: verify
verify: check test test-race test-integration e2e build ## Everything a change should pass before commit

# ---- install --------------------------------------------------------------------

.PHONY: install
install: build ## Copy bin/ply to /usr/local/bin (PREFIX=... for elsewhere; may need sudo)
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 $(BIN) $(DESTDIR)$(PREFIX)/bin/ply
	@echo "Installed. To run it at login see deploy/com.ply.server.plist (macOS) or deploy/ply.service (Linux)."

.PHONY: uninstall
uninstall: ## Remove the installed ply
	rm -f $(DESTDIR)$(PREFIX)/bin/ply

.PHONY: clean
clean: ## Remove bin/ and the web build (keeps node_modules)
	rm -f bin/ply bin/ply-*
	find web/dist -mindepth 1 ! -name .gitkeep -delete
