# Acorn

A full-screen CLI coding assistant that connects to an [Anima](https://github.com/Klace/Anima-AI) agent. Think Claude Code, but backed by a persistent AI agent with a knowledge graph that remembers you across projects.

```
 ██████╗  ██████╗ ██████╗ ██████╗ ███╗   ██╗
██╔══██╗██╔════╝██╔═══██╗██╔══██╗████╗  ██║
███████║██║     ██║   ██║██████╔╝██╔██╗ ██║
██╔══██║██║     ██║   ██║██╔══██╗██║╚██╗██║
██║  ██║╚██████╗╚██████╔╝██║  ██║██║ ╚████║
╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝
```

## What makes Acorn different

- **Persistent memory** — Anima's knowledge graph remembers you, your preferences, past projects, and lessons learned. Switch to a new codebase and the agent already knows your conventions.
- **Local tool execution** — file edits, shell commands, and searches run on *your* machine, not inside Docker. The agent thinks, your machine acts.
- **Multi-user** — multiple developers connect to the same Anima instance. Each gets their own session history and identity in the graph.
- **Plan mode** — 5-phase structured planning with environment audit, codebase scan, web research, interactive Q&A, then execution.

## Install

```bash
# From wheel
pip install acorn_cli-0.1.0-py3-none-any.whl

# Or from source
git clone https://github.com/yumlevi/acorn-cli.git
cd acorn-cli
pip install -e .
```

Requires Python 3.9+. Dependencies install automatically.

## Setup

### Server side

Your Anima agent needs the [acorn-server-side](https://github.com/Klace/Anima-AI/pull/4) branch. Add to the agent's `.env`:

```env
ANIMA_ACORN_KEY=acorn_sk_your_random_secret_here

# SearXNG for web search (recommended)
SEARXNG_URL=http://your-searxng-instance.com
```

### Client side

First run prompts for connection details:

```
$ acorn
Welcome to Acorn! Let's connect to your Anima agent.

Anima host or URL [localhost]: 192.168.1.78
Anima web port [18810]: 18810
Your username: yam
Team key (ANIMA_ACORN_KEY from server .env): acorn_sk_...

Available themes: dark, oled, light, oak, forest
Theme [dark]: forest
```

Config saved to `~/.acorn/config.toml`. Edit it anytime:

```toml
[connection]
host = "192.168.1.78"    # or https://acorn.example.com for reverse proxy
port = 18810
user = "yam"
key = "acorn_sk_..."

[display]
theme = "forest"
show_thinking = true
show_tools = true
show_usage = true
```

## Usage

```bash
# Interactive full-screen TUI
acorn

# One-shot (send message, get response, exit)
acorn "what does this project do?"

# Resume previous session
acorn --continue
acorn -c

# Start in plan mode
acorn --plan

# Override connection
acorn --host 10.0.0.5 --port 18810 --user dan
```

## Keyboard shortcuts

| Key | Action |
|-----|--------|
| **Enter** | Send message |
| **Ctrl+J** | Insert newline (multi-line input) |
| **Ctrl+P** | Toggle plan/execute mode |
| **Ctrl+C** | Stop generation (x2 to quit) |
| **Esc** | Stop generation |
| **↑↓** | Navigate autocomplete / question selector |
| **Space** | Toggle checkbox (multi-select questions) |
| **Tab** | Fill autocomplete / add notes to question |

## Slash commands

| Command | Description |
|---------|-------------|
| `/help` | Show all commands |
| `/quit` | Exit Acorn |
| `/clear` | Clear session history |
| `/stop` | Stop current generation |
| `/plan` | Toggle plan mode |
| `/status` | Connection info, session ID |
| `/theme [name]` | Switch theme (dark, oled, light, oak, forest) |
| `/mode [auto/ask/locked]` | Tool approval mode |
| `/mode rules` | Show session allow rules |
| `/approve-all` | Shortcut for `/mode auto` |
| `/bg` | List background processes |
| `/bg run <cmd>` | Run command in background |
| `/bg <id>` | View process output |
| `/bg kill <id>` | Kill a process |
| `/test [name]` | Run UI tests |
| `/test all` | Run full test suite (22 tests) |

## Permission modes

| Mode | Behavior |
|------|----------|
| **ask** (default) | Prompts for every write/exec with interactive selector. Option to add session rules (e.g. "Allow all `exec:git*`") |
| **auto** | Auto-approves everything except dangerous commands (`rm -rf`, `kill -9`, `git push --force`, etc.) |
| **locked** | Denies all writes and exec. Only reads allowed. |

When prompted, you get an arrow-key selector:
```
  ▸ ✓ Allow
    ✓ Allow all exec:git*
    ✗ Deny
```

## Plan mode

Ctrl+P toggles plan mode. The agent follows a structured 5-phase process:

1. **Environment audit** — checks installed tools, runtimes, GPU, disk space
2. **Codebase scan** — reads files, greps patterns, understands the project
3. **Research** — web searches for framework docs, API references, best practices
4. **Clarify** — interactive Q&A with single-select, multi-select, and open-ended questions
5. **Plan** — detailed step-by-step plan with file paths and verification steps

After the plan, you get:
```
  ▸ ▶ Execute plan
    ✎ Revise with feedback
    ✕ Cancel
```

Plans are saved to `.acorn/plans/` in your project directory.

## Themes

5 built-in themes with distinct visual identities:

| Theme | Background | Vibe |
|-------|-----------|------|
| **dark** | Charcoal `#1e2030` | Soft dark with lavender accents |
| **oled** | Pure black `#000000` | Monochrome, battery saver |
| **light** | White `#fafafa` | Clean with blue/green accents |
| **oak** | Warm brown `#2c2016` | Earthy with terracotta/sage |
| **forest** | Deep green `#0c1f14` | Pine/moss/emerald |

Switch with `/theme oak` — persists to config.

## Project structure

```
~/.acorn/                    # Global config (per user)
  config.toml                # Connection, theme, display prefs
  history                    # REPL command history
  last_session               # For --continue
  logs/                      # Session logs (auto-cleanup after 14 days)

.acorn/                      # Local to project (gitignored)
  config.toml                # Per-project overrides (optional)
  plans/                     # Saved approved plans
```

## Session management

Each `acorn` invocation creates a fresh session. The agent starts with no conversation history but still knows you from the knowledge graph (your preferences, expertise, past interactions).

Use `acorn -c` / `acorn --continue` to resume the previous session with full history.

Session key format: `cli:{user}@{project}-{hash}` — unique per user and project directory.

## Tool routing

| Runs locally (your machine) | Runs server-side (Anima Docker) |
|----|-----|
| `read_file`, `write_file`, `edit_file` | `graph_query`, `graph_update` |
| `exec` (shell commands) | `web_search` (SearXNG/Brave) |
| `glob`, `grep` | `message_send`, `delegate_task` |
| `web_serve` (local HTTP server) | `skill_lookup`, `save_tool` |

File operations are sandboxed to your working directory.

## Background processes

Long-running commands (servers, watchers) are auto-detected and launched in background:

```
/bg              — list all processes
/bg run <cmd>    — manual background launch
/bg 3            — view output of process #3
/bg kill 3       — stop it
```

Server-like commands (`npm start`, `flask run`, etc.) auto-background.

## Session logging

Every session writes verbose diagnostics to `~/.acorn/logs/`:

```
~/.acorn/logs/20260412-203045_cli_yam_myproject-a1b2c3d4.log
```

Contains: WebSocket events, tool requests/results with timing, permission decisions, errors with tracebacks, session summary. Auto-cleanup after 14 days.

## Testing

22 built-in tests covering all subsystems:

```
/test all                 # Run everything
/test question-parse      # Parser assertions
/test questions-inline    # Interactive selector
/test permissions         # Dangerous detection + rules
/test bg-lifecycle        # Process management
/test file-ops            # Read/write/edit roundtrip
/test shell               # Exec + safety
/test path-sandbox        # File sandboxing
/test local-server        # HTTP server start/stop
/test panels              # Theme rendering
/test themes              # Color swatches
/test env                 # Environment audit
/test connection          # WebSocket + auth
```

## Architecture

```
┌─────────────────────────┐       WebSocket        ┌──────────────────────┐
│       Acorn CLI          │ ◄════════════════════► │    Anima Agent        │
│   (developer's machine)  │                        │  (Docker container)   │
│                          │  ── chat message ──►   │                       │
│  • Full-screen TUI       │  ◄── chat:delta ───    │  • LLM reasoning      │
���  • Local tool execution  │  ◄── tool:request ──   │  • Session storage    │
│  • Context gathering     │  ── tool:result ──►    │  • Knowledge graph    │
│  • Permission system     │  ◄── chat:done ────    │  • Learning pipeline  │
│  • Themes + rendering    │                        │  • SearXNG search     │
└─────────────────────────┘                         └──────────────────────┘
```

## License

MIT
