# acorn — Go port

Go / Bubble Tea rewrite of acorn-cli. Distribution is a single static
binary per OS/arch — no Python, no Node, no runtime dependencies, launches
from any directory once dropped in `$PATH`.

## Why

The Python implementation uses Textual, which glitches on older Windows
terminals and requires a working Python install + pip/pipx. The Go port
targets:

- **Zero-install** — download one binary, `chmod +x`, done.
- **Robust Windows** — Bubble Tea uses the Windows virtual-terminal API
  directly and falls back gracefully on cmd.exe.
- **Fast startup** — compiled Go boots in tens of ms vs Python's ~1s.

## Build

Requires Go 1.22+.

```sh
cd go
go mod tidy
make build          # ./acorn
make install        # ~/.local/bin/acorn
make release        # cross-compile linux/darwin/windows × amd64/arm64 into dist/
```

## Install

One-liner installers detect your OS+arch, download the matching release
binary, verify it (ELF/Mach-O/PE magic check), atomic-rename into place,
and handle PATH setup. Re-running upgrades in place.

**Linux / macOS** (bash / zsh / fish):

```sh
curl -fsSL https://raw.githubusercontent.com/yumlevi/acorn-cli/go-rewrite/go/install.sh | sh
```

**Windows** (PowerShell 5.1+):

```powershell
irm https://raw.githubusercontent.com/yumlevi/acorn-cli/go-rewrite/go/install.ps1 | iex
```

Both scripts respect overrides via env vars:

| Env var          | Default                                            | Notes |
|------------------|----------------------------------------------------|-------|
| `ACORN_VERSION`  | `latest`                                           | Pin a tag like `v0.1.4` |
| `ACORN_DIR`      | `~/.local/bin` (Unix), `~/.acorn/bin` (Windows)    | Install to a different directory; `ACORN_DIR=/usr/local/bin sudo …` for system-wide |

### Upgrading

From inside acorn:

```
/update install
```

Or just re-run the installer one-liner from your shell — both routes
download the latest release and atomic-rename over the running binary.
On Windows the running `acorn.exe` is renamed aside as `acorn.exe.old`
first since the OS won't let you overwrite a running image.

### Manual download

If you don't want to pipe-to-shell, releases are at
<https://github.com/yumlevi/acorn-cli/releases/latest>:

```
acorn-linux-amd64       Linux  x86_64
acorn-linux-arm64       Linux  aarch64
acorn-darwin-amd64      macOS  Intel
acorn-darwin-arm64      macOS  Apple Silicon
acorn-windows-amd64.exe Windows  x64
acorn-windows-arm64.exe Windows  ARM64
```

Drop the file into a directory on `$PATH` and `chmod +x` (Unix).

## Configure

Global config at `~/.acorn/config.toml`, per-project override at
`./.acorn/config.toml` (project wins).

```toml
[connection]
host = "spore.hyrule.vip"   # or an https://… URL
port = 18801
user = "yam"
key = "<your-acorn-team-key>"

[display]
theme = "dark"              # dark, oak, forest, oled, light
show_thinking = true
show_tools = true
show_usage = true
```

If this file doesn't exist the Go port errors out and tells you to create
it. The Python port has an interactive setup wizard — run `python -m
acorn` once to go through it, or write the TOML by hand.

## Run

```sh
acorn                       # normal mode — REPL in your cwd
acorn -c                    # resume the last session (saved in ~/.acorn/last_session)
acorn --session cli:…-…-…   # resume a specific session
acorn --plan                # start in plan mode
acorn --host spore.tld --port 443 --user foo
```

## Keybindings

| Key                    | Action |
|------------------------|--------|
| Enter                  | Send message |
| Alt+Enter              | Newline in input (multi-line drafting) |
| Shift+Tab              | Toggle plan / execute mode |
| Up / Down              | Command history (when input is empty or cursor on edge line) |
| PgUp / PgDn            | Scroll chat |
| Ctrl+↑ / Ctrl+↓        | Scroll chat by one line (belt-and-braces for terminals that swallow PgUp/PgDn) |
| Shift+↑ / Shift+↓      | Same as Ctrl+↑/↓ |
| Ctrl+Home / Ctrl+End   | Jump to top / bottom of chat |
| Mouse wheel            | Scroll chat / whichever overlay is open |
| Ctrl+P                 | Toggle expanded activity panel (full-screen browser) |
| Ctrl+O                 | Toggle captured tool-output log |
| Esc                    | Close modal / overlay |
| Ctrl+C                 | While generating: stop the current turn. While idle: press twice within 1s to quit |
| Ctrl+D                 | Unconditional quit from any state |

## Slash commands

| Command | What it does |
|---------|--------------|
| `/help` | Show this list |
| `/new` | Fresh session in current cwd |
| `/clear` | Clear chat (server-side too) |
| `/resume <sessionId>` | Resume a specific session |
| `/sessions` | List saved sessions for this project |
| `/quit` | Exit |
| `/stop` | Stop the current generation |
| `/plan` | Toggle plan/execute mode (same as Shift+Tab) |
| `/status` | Connection + session info |
| `/theme [name]` | List themes or switch to one (persists to `~/.acorn/config.toml`) |
| `/mode <auto\|ask\|locked\|yolo\|rules>` | Tool approval mode |
| `/approve-all` | `/mode auto` shortcut |
| `/approve-all-dangerous` | `/mode yolo` shortcut |
| `/bg [list\|<id>\|run <cmd>\|kill <id>]` | Background process manager |
| `/update check` | Compare running version against latest GitHub release |
| `/update install [tag]` | Download + atomic-replace the running binary with the latest release (or a specific tag) |
| `/context [refresh]` | Show the project context currently sent to the agent (or reset it for the next message) |
| `/tree [depth]` | Print the project file tree (default depth 3) |
| `/init` | Create an `ACORN.md` template and append `.acorn/` to `.gitignore` |

## Feature parity vs Python acorn

**Fully ported**:

- Authentication (HTTP POST `/api/acorn/auth` → token → WS connect)
- Auto-reconnect with exponential backoff + outbox flush
- Heartbeat (15s WS ping) + disconnect detection
- Chat send + streaming deltas + thinking-delta + tool-status indicators
- Chat history replay on session join
- Plan mode with PLAN_PREFIX constant + PLAN_READY marker detection
- Plan approval modal (Execute / Revise / Cancel) + plan save to
  `.acorn/plans/plan-<ts>.md`
- Plan save silent-exception fix (prints to stderr instead of swallowing)
- QUESTIONS: prose parser with single-select `[a / b]`, multi-select `{a / b}`,
  and open-ended formats (parity with `acorn/questions.py`)
- Composition fix for plan-mode + QUESTIONS: in the same response (the
  Python 277fc8c bug — questions run first, plan modal surfaces after answers)
- Structured `ask_user` tool (new SPORE capability) with its own picker modal
- Local tool execution:
  - `read_file` / `write_file` / `edit_file` (cwd-sandboxed)
  - `glob` / `grep` (walkdir with noise-dir skipping, 500/200 result caps)
  - `exec` (inactivity timeout, dangerous-pattern block, sensitive-path block,
    output truncation, log file in `.acorn/logs/`)
- Permission system: four modes (auto/ask/locked/yolo), dangerous-pattern
  heuristics, session allow rules ("exec:git*", "write_file:src/*"),
  structured approval modal with "Allow similar" option for non-dangerous
- Session persistence — per-session JSONL at `~/.acorn/sessions/<safeid>.jsonl`
  compatible with the Python format (same character substitutions, same
  `_meta` header); `/sessions` lists, `/resume` loads
- Diagnostic log at `~/.acorn/logs/<ts>_<safeid>.log` mirroring session_log.py
- Context gathering on first message (OS, git branch, top-level project
  markers, available CLIs)
- Themes: dark, oak, forest, oled, light (runtime switchable via `/theme`)
- Companion observer protocol — outbound: `state:questions`,
  `plan:show-approval`, `plan:decided`, `plan:set-mode`, `interactive:resolved`,
  `perm:current-mode`. Inbound: `plan:decision`, `perm:query`, `perm:set-mode`
- Delegation policies: /mode pipes through the `tools.Executor.Delegation`
  field; `delegate_task` inputs are gated before the server sees them
- Slash command set matching Python's `constants.py:SLASH_COMMANDS`

**Fully ported since the initial port** (these used to be stubs):

- `/bg run|list|kill <id>` — background process manager with PDEATHSIG
  on Linux + Job Object on Windows so children die with acorn
- `/update install [tag]` — downloads the release asset, magic-byte
  verifies, atomic-renames into place (Windows moves the running exe
  aside first); capability-advertised from SPORE so acorn knows when
  the upgrade path is available
- Markdown rendering in assistant messages (via glamour, theme-aware)
- Activity side panel — shows `💭 thinking`, `⚙ tool`, `📄 read`,
  `✏ edit`, `🆕 new` entries with live previews; word-wraps thinking,
  hard-caps height to prevent flicker, cached per-entry so streaming
  doesn't re-wrap on every token. Ctrl+P opens a full-screen scrollable
  browser.
- Output-log overlay (Ctrl+O) — scrollable capture of all tool
  stdout/stderr from the session
- Structured `projectContext` wire field (replaces the old "glue
  GatherContext onto user message" path; SPORE routes it into the
  system prompt so it doesn't accumulate in `messages[]`)
- Bracketed-paste support — multi-line paste no longer fires one send
  per line (bubbletea v1.3.x)

**Intentionally out of scope**:

- `/test` harness — internal acorn UI test runner, not user-facing

## Layout

```
go/
├── Makefile                     build + cross-compile targets
├── README.md                    this file
├── install.sh                   prebuilt-binary installer
├── go.mod / go.sum              module + locked deps
├── cmd/acorn/main.go            entry point
└── internal/
    ├── config/config.go         [connection]+[display] TOML loader, last_session
    ├── conn/ws.go               auth + WS + reconnect + outbox
    ├── proto/messages.go        server message shapes
    ├── sessionlog/
    │   ├── writer.go            ~/.acorn/sessions/*.jsonl writer + listings
    │   └── debuglog.go          ~/.acorn/logs/*.log verbose logger
    ├── tools/
    │   ├── executor.go          dispatch, delegation policing, hooks
    │   ├── fileops.go           read/write/edit_file with cwd sandbox
    │   ├── search.go            glob + grep
    │   └── shell.go             exec with timeout/safety/log
    └── app/
        ├── model.go             Bubble Tea Model + init wiring
        ├── update.go            Update() — keystrokes, frames, slash
        ├── view.go              chat rendering, header, footer
        ├── themes.go            dark/oak/forest/oled/light palettes
        ├── context.go           first-message context gathering
        ├── session.go           sessionID compute, git helpers
        ├── permissions.go       TUIPerms (impl Permissions interface)
        ├── permmodal.go         per-tool approval modal
        ├── questions.go         QUESTIONS: parser + prose picker + ask_user
        ├── plan.go              plan approval modal + savePlan
        └── updater.go           /update check against GitHub releases
```

## Protocol compatibility

The Go port speaks the same protocol as the Python CLI and connects to the
same SPORE server endpoints. You can switch between the two CLIs on the
same machine — they share the `~/.acorn/config.toml`, `~/.acorn/sessions/`,
`~/.acorn/logs/`, and `~/.acorn/last_session` files. `/sessions` in either
CLI lists the same history.
