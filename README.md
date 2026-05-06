# Spore Code

Terminal coding assistant that talks to a Spore Core (SPORE) server over
WebSocket. Single static Go binary per OS/arch — no Python, no Node, no
runtime dependencies; launches from any directory once dropped in `$PATH`.

The CLI is **`spore`**; the product is **Spore Code**; the server-side
plugin is **`spore-code`**.

## Install

One-liner installers detect your OS+arch, download the matching release
binary, verify it (ELF/Mach-O/PE magic check), atomic-rename into place,
and handle PATH setup. Re-running upgrades in place.

**Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/yumlevi/spore-code/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/yumlevi/spore-code/main/install.ps1 | iex
```

Optional overrides:

| Variable                | Default                                            | Effect                  |
| ----------------------- | -------------------------------------------------- | ----------------------- |
| `SPORE_CODE_VERSION`    | `latest`                                           | Pin a tag like `v1.0.4` |
| `SPORE_CODE_DIR`        | `~/.local/bin` (Unix) / `%USERPROFILE%\.spore-code\bin` (Win) | Custom install dir |

## Usage

First launch (no `~/.spore-code/config.toml`) runs the setup wizard:
host + port → username → invite key or account password → theme. After that:

```sh
spore                       # normal mode — REPL in your cwd
spore -c                    # resume the most recent session in this cwd
spore --session cli:…-…-…   # resume a specific session
spore --plan                # start in plan mode
spore --host spore.tld --port 443 --user foo
spore logout                # clear saved credentials without launching the TUI
```

Inside the TUI, type `/help` for the full slash-command list. Highlights:

- `/init` — scaffold a `SPORE.md` template + add `.spore-code/` to `.gitignore`
- `/index` — build/refresh the per-project code index (`.spore-code/index.db`)
- `/architecture`, `/why <symbol>`, `/calls <symbol>`, `/impact` — structural code search
- `/scope strict|expanded` — toggle file-op sandbox
- `/mode auto|ask|locked|yolo|rules` — tool-approval policy
- `/scripts`, `/decisions` — graph-backed project memory (server-side)
- `/update install` — upgrade in place to the latest release
- `/logout` — clear saved credentials and exit

## Build (from source)

Requires Go 1.22+ and `zig` 0.13+ for cross-compile (CGO toolchain — the
tree-sitter language grammars vendor C code).

```sh
go mod tidy
make build          # ./spore
make install        # ~/.local/bin/spore
make release        # cross-compile linux/darwin/windows × amd64/arm64 into dist/
```

On hosts without `make` or a system C compiler, use the repo-local scripts.
They use `zig cc` for CGO when gcc/clang is unavailable:

```sh
scripts/build.sh    # ./spore
scripts/test.sh     # go test ./...
scripts/release.sh  # linux/windows amd64+arm64 into dist/
```

Builds are stamped from `git describe --tags --dirty --always` by default.
Pass `VERSION=v1.0.7` when producing binaries for a published release.
The scripts also stage the current-platform binary into
`~/.spore-code/updates`, so a running client can apply it with
`/update install local`. Set `SPORE_CODE_STAGE_UPDATE=0` to skip staging.

## Self-update

```
/update check        check the stable channel for a newer release
/update install      install the latest stable
/update install pre  install the latest pre-release
/update list         list recent releases
```

On Linux/macOS the running binary is atomically replaced via
`rename(2)`. On Windows the running `spore.exe` is renamed aside as
`spore.exe.old` first; restart to use the new version.

## Configuration

Per-project: `.spore-code/` directory holds the local code index, scratch
scripts, and per-session logs. Add `.spore-code/` to `.gitignore`
(`/init` does this for you).

Global: `~/.spore-code/config.toml`:

```toml
[connection]
host = "spore.example.com"
port = 18810
user = "yam"
auth_method = "invite"
key = "<your-spore-core-invite-key>"
password = ""

# Or for a full Spore account:
# auth_method = "password"
# key = ""
# password = "<your-spore-account-password>"

[display]
theme = "dark"

[session]
auto_resume = false
```

Run `spore` once to go through the wizard, or write the TOML by hand.
The config file is written with `0600` permissions. Password auth is stored
there so reconnects and future CLI launches can authenticate without opening
the web app.

## Releases

Four binaries per default release at
<https://github.com/yumlevi/spore-code/releases/latest>:

```
spore-linux-amd64       Linux  x86_64
spore-linux-arm64       Linux  aarch64
spore-windows-amd64.exe Windows x64
spore-windows-arm64.exe Windows ARM64
```

## Build status

Linux + Windows targets cross-compile cleanly via `zig cc`. Darwin
targets are opt-in with `INCLUDE_DARWIN=1 make release` and currently
paused for official releases — zig 0.13's bundled darwin SDK is missing
`libresolv.tbd` + the Apple frameworks Go's CGO net stack needs. Builds
will return when the build host has Apple SDK installed (or zig 0.14
ships the missing stubs).

## Compat

Pairs with a Spore Core server running the `spore-code` plugin
(`anima-new/plugins/spore-code/`). Older SPORE deployments without the
plugin will reject `/api/spore-code/auth`.

## Repo layout

```
.
├── cmd/spore/main.go            entry point
├── internal/app/                TUI (model, view, update, slash registry)
├── internal/codeindex/          tree-sitter walker + SQLite store
├── internal/conn/               WebSocket client
├── internal/tools/              local tool execution (read_file, exec, …)
├── internal/proto/              wire-protocol structs
├── internal/sessionlog/         per-session JSONL + debug logs
├── internal/config/             ~/.spore-code/config.toml read/write
├── install.sh / install.ps1     one-line installers
├── Makefile                     build + cross-compile via zig cc
└── dist/                        per-arch release binaries (gitignored)
```
