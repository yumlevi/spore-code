"""Acorn TUI — full-screen terminal app with pinned header/footer."""

import asyncio
import os
import time

from textual.app import App, ComposeResult
from textual.containers import Vertical, VerticalScroll
from textual.widgets import Static, Input, Footer, Header, RichLog
from textual.binding import Binding
from textual import work
from textual.css.query import NoMatches

from rich.text import Text
from rich.markdown import Markdown
from rich.panel import Panel
from rich.rule import Rule

from acorn.config import save_last_session, ensure_local_dir
from acorn.connection import Connection, AuthError
from acorn.context import gather_context
from acorn.permissions import Permissions
from acorn.protocol import chat_message
from acorn.session import compute_session_id, project_name, get_git_branch
from acorn.tools.executor import ToolExecutor
from acorn.themes import get_theme

PLAN_PREFIX = (
    '[MODE: Plan only. You are in planning mode.\n'
    'Phase 1 — UNDERSTAND: Read files, search the codebase, and use web_search/web_fetch as needed to fully understand the task. '
    'Ask the user clarifying questions if anything is ambiguous — do NOT assume.\n'
    'Phase 2 — PLAN: Once you have enough context, present a clear step-by-step plan of what you would change and why. '
    'Include file paths and describe each change.\n'
    'RULES: Do NOT make any changes (no write_file, edit_file, or exec). '
    'You MAY use read_file, glob, grep, web_search, and web_fetch.\n'
    'End your plan with the exact line: "PLAN_READY" on its own line so the client knows to prompt for approval.]\n\n'
)

PLAN_EXECUTE_MSG = (
    '[The user has approved the plan above. Switch to execute mode and implement it now. '
    'Proceed step by step, executing all the changes you outlined.]'
)


class AcornApp(App):
    """Full-screen Acorn TUI."""

    BINDINGS = [
        Binding('ctrl+c', 'quit_check', 'Quit', show=False),
        Binding('shift+tab', 'toggle_plan', 'Toggle Plan Mode', show=True),
        Binding('escape', 'stop_generation', 'Stop', show=False),
    ]

    CSS = """
    #header-bar {
        dock: top;
        height: 3;
        background: $surface;
        padding: 0 2;
    }
    #transcript {
        height: 1fr;
        padding: 0 1;
        scrollbar-size: 1 1;
    }
    #input-area {
        dock: bottom;
        height: auto;
        max-height: 8;
        padding: 0 1;
    }
    #mode-bar {
        dock: bottom;
        height: 1;
    }
    Input {
        border: none;
    }
    """

    def __init__(self, conn, session_id, user, theme_name, cwd, **kwargs):
        super().__init__(**kwargs)
        self.conn = conn
        self.session_id = session_id
        self.user = user
        self.theme_data = get_theme(theme_name)
        self.cwd = cwd
        self.plan_mode = False
        self.context_sent = False
        self.generating = False
        self._stream_buffer = ''
        self._last_ctrl_c = 0
        self._response_text = []

    def compose(self) -> ComposeResult:
        t = self.theme_data
        proj = project_name(self.cwd)
        branch = get_git_branch(self.cwd)

        icon = t.get('icon', '🌰')
        header_text = f" {icon} Acorn  {self.user} → {proj}"
        if branch:
            header_text += f" ({branch})"

        yield Static(header_text, id='header-bar')
        yield RichLog(id='transcript', wrap=True, highlight=True, markup=True)
        yield Static('', id='mode-bar')
        yield Input(placeholder='Send a message...', id='user-input')

    def on_mount(self):
        self._update_mode_bar()
        ensure_local_dir(self.cwd)

        # Set up tool executor with TUI-aware permissions
        self.permissions = TuiPermissions(self)
        self.executor = ToolExecutor(self.permissions, None, self.cwd)
        self.conn.tool_executor = self.executor

        # Set up message handlers
        self.conn.on('chat:delta', self._on_delta)
        self.conn.on('chat:status', self._on_status)
        self.conn.on('chat:done', self._on_done)
        self.conn.on('chat:error', self._on_error)
        self.conn.on('chat:tool', self._on_tool)
        self.conn.on('code:view', self._on_code_view)
        self.conn.on('code:diff', self._on_code_diff)
        self.conn.on('chat:start', self._on_start)

        # Focus input
        self.query_one('#user-input', Input).focus()

    def _update_mode_bar(self):
        bar = self.query_one('#mode-bar', Static)
        if self.plan_mode:
            bar.update('[white on blue] PLAN [/] research only — shift+tab to toggle')
        else:
            bar.update('[black on green] EXECUTE [/] full agent mode — shift+tab to toggle')

    def _log(self, renderable):
        try:
            self.query_one('#transcript', RichLog).write(renderable)
        except NoMatches:
            pass

    def _scroll_bottom(self):
        try:
            transcript = self.query_one('#transcript', RichLog)
            transcript.scroll_end(animate=False)
        except NoMatches:
            pass

    # ── Actions ────────────────────────────────────────────────────

    def action_toggle_plan(self):
        self.plan_mode = not self.plan_mode
        self._update_mode_bar()

    def action_quit_check(self):
        now = time.time()
        if now - self._last_ctrl_c < 1.0:
            self.exit()
        else:
            self._last_ctrl_c = now
            self._log(Text('  Press Ctrl+C again to quit', style='dim'))
            self._scroll_bottom()

    def action_stop_generation(self):
        if self.generating:
            from acorn.protocol import stop_message
            asyncio.create_task(self.conn.send(stop_message(self.session_id)))
            self._log(Text('  ⏹ Stop requested', style='dim'))
            self._scroll_bottom()

    # ── Input handling ─────────────────────────────────────────────

    async def on_input_submitted(self, event: Input.Submitted):
        text = event.value.strip()
        if not text:
            return

        inp = self.query_one('#user-input', Input)
        inp.value = ''

        # Slash commands
        if text.startswith('/'):
            await self._handle_command(text)
            return

        # Show user message
        t = self.theme_data
        self._log(Text(f'\n  {self.user}', style=f'bold {t["prompt_user"]}'))
        self._log(Text(f'  {text}', style=''))
        self._log(Text(''))

        # Build content
        content = text
        if not self.context_sent:
            ctx = gather_context(self.cwd)
            content = ctx + '\n\n' + text
            self.context_sent = True

        if self.plan_mode:
            content = PLAN_PREFIX + content

        # Send
        self._stream_buffer = ''
        self._response_text = []
        self.generating = True
        await self.conn.send(chat_message(self.session_id, content, self.user))

    async def _handle_command(self, text):
        parts = text.split(None, 1)
        cmd = parts[0].lower()
        args = parts[1] if len(parts) > 1 else ''

        if cmd in ('/quit', '/exit'):
            self.exit()
        elif cmd == '/clear':
            from acorn.protocol import clear_message
            await self.conn.send(clear_message(self.session_id))
            self.context_sent = False
            transcript = self.query_one('#transcript', RichLog)
            transcript.clear()
            self._log(Text('  Session cleared', style='dim'))
        elif cmd == '/plan':
            self.action_toggle_plan()
        elif cmd == '/status':
            self._log(Text(
                f'  User: {self.user}\n'
                f'  Session: {self.session_id}\n'
                f'  Server: {self.conn.host}:{self.conn.port}\n'
                f'  CWD: {self.cwd}',
            ))
        elif cmd == '/theme':
            from acorn.themes import list_themes
            if args:
                available = list_themes()
                if args in available:
                    self.theme_data = get_theme(args)
                    self._update_mode_bar()
                    self._log(Text(f'  Theme changed to {args}', style='dim'))
                else:
                    self._log(Text(f'  Unknown theme. Available: {", ".join(available)}', style='dim'))
            else:
                self._log(Text(f'  Current: {self.theme_data["name"]}. Available: {", ".join(list_themes())}', style='dim'))
        elif cmd == '/approve-all':
            self.permissions.approve_all = True
            self._log(Text('  All tool executions will be auto-approved', style='yellow'))
        elif cmd == '/help':
            self._log(Panel(
                '/help            Show this help\n'
                '/quit            Exit Acorn\n'
                '/clear           Clear session\n'
                '/stop or Esc     Stop generation\n'
                '/plan            Toggle plan mode\n'
                '/status          Connection info\n'
                '/theme [name]    Switch theme\n'
                '/approve-all     Auto-approve tools\n'
                '\n'
                'Shift+Tab        Toggle plan/execute\n'
                'Ctrl+C Ctrl+C    Quit\n'
                'Esc              Stop generation',
                title='Acorn Commands',
                border_style='dim',
            ))
        else:
            self._log(Text(f'  Unknown command: {cmd}', style='red'))
        self._scroll_bottom()

    # ── WebSocket event handlers ───────────────────────────────────

    async def _on_start(self, msg):
        pass

    async def _on_delta(self, msg):
        text = msg.get('text', '')
        self._stream_buffer += text
        self._response_text.append(text)
        # Re-render the full streamed response as markdown
        try:
            transcript = self.query_one('#transcript', RichLog)
            # Remove last line (previous partial render) and re-render
            # For simplicity, just write deltas as plain text for now
            # Full markdown render happens on chat:done
        except NoMatches:
            pass

    async def _on_status(self, msg):
        t = self.theme_data
        status = msg.get('status', '')
        if status == 'thinking_start':
            self._log(Text('  ● Thinking...', style=t['thinking']))
            self._scroll_bottom()
        elif status == 'thinking':
            pass  # don't spam
        elif status == 'thinking_done':
            pass
        elif status == 'tool_exec_start':
            tool = msg.get('tool', '')
            detail = msg.get('detail', '')
            self._log(Text(f'  ┌ ⚙ {tool} {detail[:80]}', style=t['tool_icon']))
            self._scroll_bottom()
        elif status == 'tool_exec_done':
            parts = []
            if msg.get('durationMs'):
                parts.append(f'{msg["durationMs"]}ms')
            if msg.get('resultChars'):
                parts.append(f'{msg["resultChars"]:,} chars')
            self._log(Text(f'  └ ✓ {" · ".join(parts)}', style=t['tool_done']))
            self._scroll_bottom()

    async def _on_code_view(self, msg):
        t = self.theme_data
        path = msg.get('path', '')
        lines = msg.get('content', '').count('\n') + 1
        is_new = msg.get('isNew', False)
        label = 'new' if is_new else 'read'
        self._log(Text(f'  │ {label} {path} ({lines} lines)', style=t['read_icon']))
        self._scroll_bottom()

    async def _on_code_diff(self, msg):
        t = self.theme_data
        path = msg.get('path', '')
        self._log(Text(f'  │ edit {path}', style=t['edit_icon']))
        self._scroll_bottom()

    async def _on_done(self, msg):
        self.generating = False
        response = ''.join(self._response_text)
        t = self.theme_data

        # Render full response as markdown
        if response.strip():
            try:
                self._log(Markdown(response))
            except Exception:
                self._log(Text(response))

        # Usage stats
        usage = msg.get('usage', {})
        if usage:
            inp = usage.get('input_tokens', 0)
            out = usage.get('output_tokens', 0)
            parts = [f'{inp:,} in', f'{out:,} out']
            iters = msg.get('iterations')
            if iters and iters > 1:
                parts.append(f'{iters} iters')
            tool_usage = msg.get('toolUsage', {})
            if tool_usage:
                total = sum(tool_usage.values())
                if total:
                    parts.append(f'{total} tools')
            self._log(Text(f'  {"  ".join(parts)}', style=t['usage']))

        self._log(Text(''))  # spacing
        self._scroll_bottom()

        # Plan mode: check if plan is ready
        if self.plan_mode and response and ('PLAN_READY' in response or len(response) > 500):
            self._log(Text('  Plan ready — type "execute" to run it, or provide feedback', style=t['accent']))
            self._scroll_bottom()

        self._stream_buffer = ''
        self._response_text = []

    async def _on_error(self, msg):
        self.generating = False
        error = msg.get('error', 'Unknown error')
        self._log(Panel(f'[bold red]{error}[/bold red]', title='Error', border_style='red'))
        self._scroll_bottom()

    async def _on_tool(self, msg):
        pass


class TuiPermissions:
    """Permissions that work with the TUI — auto-approve for now."""
    def __init__(self, app):
        self.app = app
        self.approve_all = False

    def is_auto_approved(self, tool_name, input):
        if self.approve_all:
            return True
        return tool_name in {'read_file', 'glob', 'grep'}

    async def prompt(self, tool_name, input):
        if self.approve_all:
            return True
        # In TUI mode, log the request and auto-approve with notification
        summary = tool_name
        if tool_name == 'exec':
            summary = f'exec: {input.get("command", "")[:80]}'
        elif tool_name in ('write_file', 'edit_file'):
            summary = f'{tool_name}: {input.get("path", "")}'
        self.app._log(Text(f'  ⚡ Auto-approved: {summary}', style='yellow'))
        self.app._scroll_bottom()
        return True
