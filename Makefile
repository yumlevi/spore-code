# Cross-compiles the acorn CLI for every supported target. Output goes to
# ./dist/ as `acorn-<os>-<arch>[.exe]`. Strip debug info for smaller binaries.
#
# v0.6.0+ requires cgo (tree-sitter language grammars are vendored C
# code that the smacker bindings link in). We use `zig cc` as the
# C cross-compiler so a single Linux dev box can produce all 6 release
# binaries without per-target gcc/clang toolchains. Linux targets link
# against musl and emit fully-static binaries; macOS and Windows
# targets emit standard cgo binaries (statically linked at the C-rt
# layer; macOS still needs the host libSystem at runtime, but every
# Mac ships it).

BIN = acorn
VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)
LDFLAGS_STATIC = $(LDFLAGS) -extldflags '-static'

# Per-target zig triples. Linux uses musl for static linking;
# macOS uses the standard target (still single-binary at install time;
# system libSystem is always present). Windows uses the gnu (mingw)
# triple.
ZIG_LINUX_AMD64   = x86_64-linux-musl
ZIG_LINUX_ARM64   = aarch64-linux-musl
ZIG_DARWIN_AMD64  = x86_64-macos-none
ZIG_DARWIN_ARM64  = aarch64-macos-none
ZIG_WINDOWS_AMD64 = x86_64-windows-gnu
ZIG_WINDOWS_ARM64 = aarch64-windows-gnu

.PHONY: all build clean release install tidy run

all: build

# Local dev build — uses host gcc/cc + cgo. zig works too if you set
# CC=zig cc explicitly.
build:
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/acorn

run: build
	./$(BIN)

tidy:
	go mod tidy

# Cross-compile every target via zig cc. Linux targets are
# fully-static (musl); macOS and Windows are dynamically-linked
# against the host C runtime which is always present on those
# platforms. End user installs a single binary either way.
release: clean
	@command -v zig >/dev/null 2>&1 || { echo "zig not found in PATH — install from https://ziglang.org/download/ (need 0.13+) for cgo cross-compile"; exit 1; }
	mkdir -p dist
	@$(MAKE) --no-print-directory release-one GOOS=linux   GOARCH=amd64 ZIG_TARGET=$(ZIG_LINUX_AMD64)   STATIC=1
	@$(MAKE) --no-print-directory release-one GOOS=linux   GOARCH=arm64 ZIG_TARGET=$(ZIG_LINUX_ARM64)   STATIC=1
	@$(MAKE) --no-print-directory release-one GOOS=darwin  GOARCH=amd64 ZIG_TARGET=$(ZIG_DARWIN_AMD64)
	@$(MAKE) --no-print-directory release-one GOOS=darwin  GOARCH=arm64 ZIG_TARGET=$(ZIG_DARWIN_ARM64)
	@$(MAKE) --no-print-directory release-one GOOS=windows GOARCH=amd64 ZIG_TARGET=$(ZIG_WINDOWS_AMD64) EXT=.exe
	@$(MAKE) --no-print-directory release-one GOOS=windows GOARCH=arm64 ZIG_TARGET=$(ZIG_WINDOWS_ARM64) EXT=.exe
	@echo "done — $$(ls -1 dist/ | wc -l) binaries in dist/"

# Internal sub-make — builds one target binary using zig as the CC.
# Set STATIC=1 to fully static-link (Linux only). EXT for Windows .exe.
release-one:
	@out="dist/$(BIN)-$(GOOS)-$(GOARCH)$(EXT)"; \
	echo "→ $$out  (zig cc -target $(ZIG_TARGET))"; \
	if [ "$(STATIC)" = "1" ]; then \
		extflags="-extldflags '-static'"; \
	else \
		extflags=""; \
	fi; \
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=1 \
	    CC="zig cc -target $(ZIG_TARGET)" \
	    CXX="zig c++ -target $(ZIG_TARGET)" \
	    go build -ldflags "$(LDFLAGS) $$extflags" -o $$out ./cmd/acorn

install: build
	install -Dm755 $(BIN) $${HOME}/.local/bin/$(BIN)
	@echo "installed to $${HOME}/.local/bin/$(BIN)"

clean:
	rm -rf dist $(BIN)
