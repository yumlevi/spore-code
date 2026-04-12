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
        self._live: None = None
        self._buffer = ''

    def start_streaming(self):
        self._buffer = ''
        self._live = Live(console=self.console, refresh_per_second=8, vertical_overflow='visible')
        self._live.start()

    def stream_delta(self, text: str):
        self._buffer += text
        if self._live:
            try:
                self._live.update(Markdown(self._buffer))
            except Exception:
                self._live.update(Text(self._buffer))

    def finish_streaming(self, usage=None, iterations=None, tool_usage=None):
        if self._live:
            try:
                self._live.update(Markdown(self._buffer))
            except Exception:
                self._live.update(Text(self._buffer))
            self._live.stop()
            self._live = None
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
        if tokens:
            self.console.print(f'[dim italic]  Thinking... ({tokens} tokens)[/dim italic]', end='\r')
        else:
            self.console.print('[dim italic]  Thinking...[/dim italic]', end='\r')

    def clear_thinking(self):
        self.console.print(' ' * 60, end='\r')

    def show_tool_start(self, name: str, detail: str):
        self.console.print(f'  [yellow]⚙ {name}[/yellow] [dim]{detail[:100]}[/dim]')

    def show_tool_done(self, name: str, result_chars: int = 0, duration_ms: int = 0):
        parts = []
        if duration_ms:
            parts.append(f'{duration_ms}ms')
        if result_chars:
            parts.append(f'{result_chars:,} chars')
        self.console.print(f'  [green]✓[/green] [dim]{" · ".join(parts)}[/dim]')

    def show_diff(self, path: str, old_text: str, new_text: str):
        # Compact diff — show path and a few lines
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

    def show_code_view(self, path: str, content: str, language: str = 'text', is_new: bool = False):
        # Compact — just show path and line count, not the full file
        lines = content.count('\n') + 1
        label = 'new' if is_new else 'read'
        self.console.print(f'  [blue]{label}[/blue] {path} [dim]({lines} lines)[/dim]')

    def show_error(self, msg: str):
        self.console.print(f'[bold red]Error:[/bold red] {msg}')

    def show_info(self, msg: str):
        self.console.print(f'[dim]{msg}[/dim]')
