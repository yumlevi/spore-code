"""Terminal output rendering using Rich."""

from rich.console import Console
from rich.live import Live
from rich.markdown import Markdown
from rich.panel import Panel
from rich.syntax import Syntax
from rich.text import Text


class Renderer:
    def __init__(self, console: Console = None):
        self.console = console or Console()
        self._live = None
        self._buffer = ''
        self._thinking = False
        self._thinking_tokens = 0

    def _update_live(self):
        """Update the Live display with current state."""
        if not self._live:
            return
        if self._buffer:
            try:
                self._live.update(Markdown(self._buffer))
            except Exception:
                self._live.update(Text(self._buffer))
        elif self._thinking:
            tok = f' ({self._thinking_tokens} tokens)' if self._thinking_tokens else ''
            self._live.update(Text(f'  Thinking...{tok}', style='dim italic'))

    def _stop_live(self):
        """Temporarily stop Live so we can print static lines (tool events, etc.)."""
        if self._live:
            self._live.stop()
            self._live = None

    def _ensure_live(self):
        """Restart Live if needed (after printing static lines)."""
        if not self._live:
            self._live = Live(console=self.console, refresh_per_second=8, vertical_overflow='visible')
            self._live.start()
            self._update_live()

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
            self.console.print(f'[dim]{" · ".join(parts)}[/dim]')
        self.console.print()

    def show_thinking(self, tokens=0):
        self._thinking = True
        self._thinking_tokens = tokens
        self._update_live()

    def clear_thinking(self):
        self._thinking = False
        self._thinking_tokens = 0
        self._update_live()

    def show_tool_start(self, name: str, detail: str):
        self._stop_live()
        self.console.print(f'  [yellow]⚙ {name}[/yellow] [dim]{detail[:100]}[/dim]')
        self._ensure_live()

    def show_tool_done(self, name: str, result_chars: int = 0, duration_ms: int = 0):
        self._stop_live()
        parts = []
        if duration_ms:
            parts.append(f'{duration_ms}ms')
        if result_chars:
            parts.append(f'{result_chars:,} chars')
        self.console.print(f'  [green]✓[/green] [dim]{" · ".join(parts)}[/dim]')
        self._ensure_live()

    def show_diff(self, path: str, old_text: str, new_text: str):
        self._stop_live()
        old_lines = old_text.strip().split('\n')
        new_lines = new_text.strip().split('\n')
        preview_lines = []
        for line in old_lines[:3]:
            preview_lines.append(f'[red]- {line[:120]}[/red]')
        if len(old_lines) > 3:
            preview_lines.append(f'[dim]  ... ({len(old_lines) - 3} more lines)[/dim]')
        for line in new_lines[:3]:
            preview_lines.append(f'[green]+ {line[:120]}[/green]')
        if len(new_lines) > 3:
            preview_lines.append(f'[dim]  ... ({len(new_lines) - 3} more lines)[/dim]')
        self.console.print(f'  [yellow]edit[/yellow] {path}')
        for line in preview_lines:
            self.console.print(f'    {line}')
        self._ensure_live()

    def show_code_view(self, path: str, content: str, language: str = 'text', is_new: bool = False):
        self._stop_live()
        lines = content.count('\n') + 1
        label = 'new' if is_new else 'read'
        self.console.print(f'  [blue]{label}[/blue] {path} [dim]({lines} lines)[/dim]')
        self._ensure_live()

    def show_error(self, msg: str):
        self._stop_live()
        self.console.print(f'[bold red]Error:[/bold red] {msg}')

    def show_info(self, msg: str):
        self.console.print(f'[dim]{msg}[/dim]')
