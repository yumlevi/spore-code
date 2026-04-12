"""Terminal output rendering using Rich — theme-aware with visual structure."""

from rich.console import Console
from rich.live import Live
from rich.markdown import Markdown
from rich.panel import Panel
from rich.rule import Rule
from rich.syntax import Syntax
from rich.text import Text
from rich.columns import Columns
from rich.table import Table

from acorn.themes import get_theme, DEFAULT_THEME


class Renderer:
    def __init__(self, console: Console = None, theme_name: str = DEFAULT_THEME):
        self.console = console or Console()
        self.theme = get_theme(theme_name)
        self._live = None
        self._buffer = ''
        self._thinking = False
        self._thinking_tokens = 0

    # ── Live display management ────────────────────────────────────

    def _update_live(self):
        if not self._live:
            return
        if self._buffer:
            try:
                self._live.update(Markdown(self._buffer))
            except Exception:
                self._live.update(Text(self._buffer))
        elif self._thinking:
            tok = f' ({self._thinking_tokens} tokens)' if self._thinking_tokens else ''
            t = self.theme
            self._live.update(Text(f'  ● Thinking...{tok}', style=t['thinking']))

    def _stop_live(self):
        if self._live:
            self._live.stop()
            self._live = None

    def _ensure_live(self):
        if not self._live:
            self._live = Live(console=self.console, refresh_per_second=8, vertical_overflow='visible')
            self._live.start()
            self._update_live()

    # ── Streaming ──────────────────────────────────────────────────

    def start_streaming(self):
        self._buffer = ''
        self._thinking = False
        self._thinking_tokens = 0
        self._live = Live(console=self.console, refresh_per_second=8, vertical_overflow='visible')
        self._live.start()

    def stream_delta(self, text: str):
        self._thinking = False
        self._buffer += text
        self._update_live()

    def finish_streaming(self, usage=None, iterations=None, tool_usage=None):
        self._stop_live()
        t = self.theme
        if usage:
            inp = usage.get('input_tokens', 0)
            out = usage.get('output_tokens', 0)
            parts = [f'{inp:,} in', f'{out:,} out']
            if iterations and iterations > 1:
                parts.append(f'{iterations} iters')
            if tool_usage:
                total_tools = sum(tool_usage.values())
                if total_tools:
                    parts.append(f'{total_tools} tools')
            self.console.print(f'  [{t["usage"]}]{"  ".join(parts)}[/{t["usage"]}]')
        self.console.print()

    # ── Thinking ───────────────────────────────────────────────────

    def show_thinking(self, tokens=0):
        self._thinking = True
        self._thinking_tokens = tokens
        self._update_live()

    def clear_thinking(self):
        self._thinking = False
        self._thinking_tokens = 0
        self._update_live()

    # ── Tool execution ─────────────────────────────────────────────

    def show_tool_start(self, name: str, detail: str):
        self._stop_live()
        t = self.theme
        self.console.print(
            f'  [{t["tool_icon"]}]┌ ⚙ {name}[/{t["tool_icon"]}] [{t["muted"]}]{detail[:100]}[/{t["muted"]}]'
        )
        self._ensure_live()

    def show_tool_done(self, name: str, result_chars: int = 0, duration_ms: int = 0):
        self._stop_live()
        t = self.theme
        parts = []
        if duration_ms:
            parts.append(f'{duration_ms}ms')
        if result_chars:
            parts.append(f'{result_chars:,} chars')
        self.console.print(
            f'  [{t["tool_done"]}]└ ✓[/{t["tool_done"]}] [{t["muted"]}]{" · ".join(parts)}[/{t["muted"]}]'
        )
        self._ensure_live()

    def show_diff(self, path: str, old_text: str, new_text: str):
        self._stop_live()
        t = self.theme
        old_lines = old_text.strip().split('\n')
        new_lines = new_text.strip().split('\n')

        diff_content = []
        for line in old_lines[:4]:
            diff_content.append(f'[{t["diff_del"]}]- {line[:120]}[/{t["diff_del"]}]')
        if len(old_lines) > 4:
            diff_content.append(f'[{t["muted"]}]  ... ({len(old_lines) - 4} more)[/{t["muted"]}]')
        for line in new_lines[:4]:
            diff_content.append(f'[{t["diff_add"]}]+ {line[:120]}[/{t["diff_add"]}]')
        if len(new_lines) > 4:
            diff_content.append(f'[{t["muted"]}]  ... ({len(new_lines) - 4} more)[/{t["muted"]}]')

        self.console.print(Panel(
            '\n'.join(diff_content),
            title=f'[{t["edit_icon"]}] edit: {path}',
            border_style=t['tool_border'],
            padding=(0, 1),
        ))
        self._ensure_live()

    def show_code_view(self, path: str, content: str, language: str = 'text', is_new: bool = False):
        self._stop_live()
        t = self.theme
        lines = content.count('\n') + 1
        label = 'new' if is_new else 'read'
        icon_style = t['tool_done'] if is_new else t['read_icon']
        self.console.print(
            f'  [{icon_style}]│ {label}[/{icon_style}] {path} [{t["muted"]}]({lines} lines)[/{t["muted"]}]'
        )
        self._ensure_live()

    # ── Messages ───────────────────────────────────────────────────

    def show_error(self, msg: str):
        self._stop_live()
        t = self.theme
        self.console.print(Panel(
            f'[{t["error"]}]{msg}[/{t["error"]}]',
            title='[bold red]Error',
            border_style='red',
            padding=(0, 1),
        ))

    def show_info(self, msg: str):
        self.console.print(f'  [{self.theme["info"]}]{msg}[/{self.theme["info"]}]')

    # ── Banner ─────────────────────────────────────────────────────

    def show_banner(self, user: str, project: str):
        t = self.theme
        icon = t.get('icon', '🌰')

        header = Text()
        header.append(f' {icon} Acorn ', style=t['banner'])
        header.append('  ', style='')
        header.append(user, style=t['prompt_user'])
        header.append(' → ', style=t['muted'])
        header.append(project, style=t['prompt_project'])

        self.console.print(Panel(
            header,
            border_style=t['banner_border'],
            padding=(0, 1),
        ))
        self.console.print(f'  [{t["banner_sub"]}]/help · Shift+Tab toggle plan mode · /quit[/{t["banner_sub"]}]')
        self.console.print()

    def show_separator(self, label: str = ''):
        t = self.theme
        if label:
            self.console.print(Rule(label, style=t['separator']))
        else:
            self.console.print(Rule(style=t['separator']))
