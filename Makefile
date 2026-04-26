# Cross-compiles the acorn CLI for every supported target. Output goes to
# ./dist/ as `acorn-<os>-<arch>[.exe]`. Strip debug info for smaller binaries.

BIN = acorn
VERSION ?= 0.1.0
LDFLAGS = -s -w -X main.version=$(VERSION)

PLATFORMS = \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/arm64

.PHONY: all build clean release install tidy run

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/acorn

run: build
	./$(BIN)

tidy:
	go mod tidy

release: clean
	mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ $$os = "windows" ] && ext=".exe"; \
		out="dist/$(BIN)-$$os-$$arch$$ext"; \
		echo "→ $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build \
			-ldflags "$(LDFLAGS)" -o $$out ./cmd/acorn || exit 1; \
	done
	@echo "done — $$(ls -1 dist/ | wc -l) binaries in dist/"

install: build
	install -Dm755 $(BIN) $${HOME}/.local/bin/$(BIN)
	@echo "installed to $${HOME}/.local/bin/$(BIN)"

clean:
	rm -rf dist $(BIN)
