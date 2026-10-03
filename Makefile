# falcon-asm - language server for GAS (AT&T) x86 assembly.
#
# All build output lands under build/: binaries in build/bin/, coverage in
# build/coverage/, the pinned upstream data checkouts used by xedgen in
# build/xed and build/asm-lsp. Pure ASCII, per project convention.
#
# make            build the server into build/bin/
# make test       go tests under -race
# make coverage   tests with a statement-coverage profile and the 80% gate
# make analyze    go vet, gofmt check, golangci-lint when installed
# make check      ascii-check over the tracked tree
# make vscode     build and package the VS Code client (.vsix)
# make install    install the server into $(PREFIX)/bin
# make xed-data   regenerate internal/isa/data/xed.json from pinned upstreams
# make bench      Go microbenchmarks into build/bench/bench.txt
# make lspbench   end-to-end LSP benchmark into build/bench/lspbench.{txt,json}

# pipefail so a failing benchmark is not hidden behind tee's exit status.
SHELL       := /bin/bash
.SHELLFLAGS := -o pipefail -c

VARIANT     ?= release
PREFIX      ?= $(HOME)/.local
GO          ?= go
COVERAGE_MIN ?= 80

BUILD_DIR   := build
BIN_DIR     := $(BUILD_DIR)/bin
COV_DIR     := $(BUILD_DIR)/coverage
BENCH_DIR   := $(BUILD_DIR)/bench

# Benchmark knobs: BENCH selects benchmarks (a go test -bench regexp),
# BENCH_COUNT > 1 gives benchstat enough samples, LSPBENCH_ARGS passes
# flags to tools/lspbench (e.g. -root=../airies-boot -scenarios=typing).
BENCH         ?= .
BENCH_COUNT   ?= 1
BENCH_TIME    ?= 1s
LSPBENCH_ARGS ?=
# Only packages that define benchmarks, so a run does not build and start
# a test binary per package for nothing.
BENCH_PKGS := $(sort $(patsubst %/,./%,$(dir $(shell grep -rl --include='*_test.go' '^func Benchmark' internal cmd tools))))

VERSION      := $(shell tr -d '\n' < release_version)
BUILD_STAMP  := $(shell date -u +%Y%m%d.%H%M)
FULL_VERSION := $(VERSION)_$(BUILD_STAMP).$(VARIANT)

GO_LDFLAGS := -s -w -X main.version=$(FULL_VERSION) -X main.buildTime=$(BUILD_STAMP)
ifeq ($(VARIANT),debug)
GO_FLAGS   :=
GO_LDFLAGS := -X main.version=$(FULL_VERSION) -X main.buildTime=$(BUILD_STAMP)
else
GO_FLAGS   := -trimpath
endif

SERVER_BIN := $(BIN_DIR)/falcon-asm

.DEFAULT_GOAL := all
.PHONY: all server test test-junit coverage analyze check vscode install \
        uninstall xed-data clean version bench lspbench

all: server

version:
	@echo "$(FULL_VERSION)"

server: | $(BIN_DIR)
	$(GO) build $(GO_FLAGS) -ldflags '$(GO_LDFLAGS)' -o $(SERVER_BIN) ./cmd/falcon-asm

$(BIN_DIR) $(COV_DIR) $(BENCH_DIR):
	mkdir -p $@

test:
	$(GO) test -race ./...

# The gate reads the total from `go tool cover` rather than trusting a
# per-package number, so a well-covered package cannot hide a bare one.
coverage: | $(COV_DIR)
	$(GO) test -race -covermode=atomic -coverprofile=$(COV_DIR)/go.out ./...
	@$(GO) tool cover -func=$(COV_DIR)/go.out | tail -1
	@$(GO) tool cover -func=$(COV_DIR)/go.out | awk -v min=$(COVERAGE_MIN) \
	    '/^total:/ { sub("%","",$$3); if ($$3+0 < min) { printf "coverage %s%% is below %s%%\n", $$3, min; exit 1 } }'

analyze:
	$(GO) vet ./...
	@out=$$(gofmt -l $$(git ls-files -co --exclude-standard '*.go')); \
	    if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; \
	    else echo "golangci-lint not installed; CI runs it"; fi

check:
	ascii-check -r .

# Output is kept whole (tee) so two runs can be compared with benchstat.
bench: | $(BENCH_DIR)
	$(GO) test -run='^$$' -bench='$(BENCH)' -benchmem -count=$(BENCH_COUNT) \
	    -benchtime=$(BENCH_TIME) $(BENCH_PKGS) | tee $(BENCH_DIR)/bench.txt

lspbench: server | $(BENCH_DIR)
	$(GO) build -o $(BIN_DIR)/lspbench ./tools/lspbench
	$(BIN_DIR)/lspbench -server $(SERVER_BIN) -json $(BENCH_DIR)/lspbench.json \
	    $(LSPBENCH_ARGS) | tee $(BENCH_DIR)/lspbench.txt

vscode:
	cd editors/vscode && npm ci && npm test && npm run package

install: server
	install -Dm755 $(SERVER_BIN) $(DESTDIR)$(PREFIX)/bin/falcon-asm

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/falcon-asm

# Upstream data is pinned in tools/xedgen/README.md. The checkouts live under
# build/ and are never needed for a normal build: xed.json is committed.
xed-data:
	$(GO) run ./tools/xedgen -xed $(BUILD_DIR)/xed -asmlsp $(BUILD_DIR)/asm-lsp \
	    -out internal/isa/data/xed.json

clean:
	rm -rf $(BUILD_DIR)
