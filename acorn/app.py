"""Acorn TUI — full-screen terminal app with pinned header/footer."""

import asyncio
import os
import time

# Ensure truecolor so custom theme backgrounds aren't mapped to black.
if not os.environ.get("COLORTERM"):
    os.environ["COLORTERM"] = "truecolor"

from textual.app import App, ComposeResult
from textual.containers import Vertical, VerticalScroll
from textual.widgets import Static, Input, RichLog
from textual.binding import Binding
from textual.css.query import NoMatches

from rich.text import Text
from rich.markdown import Markdown
from rich.panel import Panel
from rich.rule import Rule
from rich.table import Table
from rich.console import Group

from acorn.config import save_last_session, ensure_local_dir, load_config, save_config
from acorn.connection import Connection, AuthError
from acorn.context import gather_context
from acorn.permissions import Permissions
from acorn.protocol import chat_message
from acorn.session import compute_session_id, project_name, get_git_branch
from acorn.tools.executor import ToolExecutor
from acorn.themes import get_theme
from acorn.questions import parse_questions, format_answers
from acorn.background import ProcessManager
import acorn.commands.test  # noqa: F401 — registers /test command
import acorn.commands.bg    # noqa: F401 — registers /bg command

PLAN_PREFIX = (
    '[MODE: Plan only. You are in planning mode. Follow these phases in order:\n\n'

    'PHASE 1 — ENVIRONMENT AUDIT:\n'
    'The context above includes the local environment (OS, installed tools, runtimes). '
    'Review what is available. If the task requires tools/runtimes not installed, note them. '
    'Use exec to check versions or configs if needed (e.g. `node --version`, `cat package.json`).\n\n'

    'PHASE 2 — CODEBASE SCAN:\n'
    'Use read_file, glob, and grep to understand the existing codebase structure, patterns, '
    'conventions, config files, and dependencies. Identify what exists and what needs to change.\n\n'

    'PHASE 3 — RESEARCH:\n'
    'Identify topics you need more context on — frameworks, APIs, libraries, best practices. '
    'Use web_search and web_fetch to research them. For example:\n'
    '  - "Next.js 14 app router best practices 2024"\n'
    '  - "Tailwind CSS v4 setup guide"\n'
    '  - API docs for libraries you plan to use\n'
    'Do actual searches — don\'t rely on stale training knowledge for fast-moving tools.\n\n'

    'PHASE 4 — CLARIFY:\n'
    'If anything is still ambiguous, ask the user using this format:\n\n'
    'QUESTIONS:\n'
    '1. Single-select question? [Option A / Option B / Option C]\n'
    '2. Multi-select question? {Option A / Option B / Option C / Option D}\n'
    '3. Open-ended question?\n\n'
    '[brackets] = single-select, {braces} = multi-select checkboxes, no brackets = open text. '
    'Users can press Tab on any answer to add notes. Only ask if genuinely needed.\n\n'

    'PHASE 5 — PLAN:\n'
    'Present a detailed plan with:\n'
    '  - Prerequisites (what needs to be installed/configured first)\n'
    '  - Step-by-step changes with file paths\n'
    '  - New files to create vs existing files to modify\n'
    '  - Dependencies to install\n'
    '  - Any commands to run\n'
    '  - How to verify it works\n\n'

    'RULES:\n'
    '- Do NOT make changes (no write_file, edit_file).\n'
    '- Do NOT run destructive or modifying commands.\n'
    '- You MAY use: read_file, glob, grep, web_search, web_fetch, exec (read-only commands only like ls, cat, which, --version).\n'
    '- End your plan with "PLAN_READY" on its own line.]\n\n'
)

PLAN_EXECUTE_MSG = (
    '[The user has approved the plan above. Switch to execute mode and implement it now. '
    'Proceed step by step, executing all the changes you outlined.]'
)


def _to_hex(color_str):
    """Extract a hex color from a Rich style string. Falls back to a default."""
    import re
    if not color_str:
        return None
    m = re.search(r'#[0-9a-fA-F]{6}', color_str)
    if m:
        return m.group(0)
    return None


def _register_acorn_themes(app):
    """Register our themes as native Textual themes so backgrounds actually work."""
    from textual.theme import Theme as TextualTheme
    from acorn.themes import THEMES

    for name, t in THEMES.items():
        is_dark = name != 'light'
        primary = _to_hex(t['accent']) or '#89b4fa'
        app.register_theme(TextualTheme(
            name=f'acorn-{name}',
            primary=primary,
            secondary=_to_hex(t.get('accent2')),
            background=t['bg'],
            surface=t['bg_header'],
            panel=t['bg_panel'],
            foreground=t['fg'],
            warning=_to_hex(t.get('warning')),
            error=_to_hex(t.get('error')),
            success=_to_hex(t.get('success')),
            accent=_to_hex(t.get('accent')),
            dark=is_dark,
        ))


class AcornApp(App):
    """Full-screen Acorn TUI."""

    BINDINGS = [
        Binding('ctrl+c', 'quit_check', 'Quit', show=False),
        Binding('ctrl+p', 'toggle_plan', 'Plan Mode', show=True, priority=True),
        Binding('escape', 'stop_generation', 'Stop', show=False),
    ]

    CSS = """
    Screen {
        background: $background;
        color: $foreground;
    }
    #header-bar {
        dock: top;
        height: auto;
        max-height: 12;
        padding: 0 1;
        background: $surface;
        color: $foreground;
        border-bottom: solid $accent;
    }
    #header-bar.collapsed {
        height: 1;
        max-height: 1;
    }
    #main-scroll {
        height: 1fr;
        background: $background;
    }
    #transcript {
        height: auto;
        padding: 0 1;
        background: $background;
        color: $foreground;
    }
    #stream-area {
        height: auto;
        padding: 0 2;
        margin: 0 1;
        background: $background;
    }
    #bottom-area {
        dock: bottom;
        height: auto;
        max-height: 8;
        background: $surface;
        border-top: solid $accent;
    }
    #user-input {
        height: 3;
        padding: 0 1;
        background: $surface;
        color: $foreground;
    }
    #footer-bar {
        height: 3;
        width: 100%;
        background: $surface;
        padding: 0 1;
    }
    Input {
        background: $surface;
        color: $foreground;
    }
    Input:focus {
        border: tall $accent;
    }
    RichLog {
        background: $background;
        color: $foreground;
    }
    """

    LOGO_FULL = r"""
     ██████╗  ██████╗ ██████╗ ██████╗ ███╗   ██╗
    ██╔══██╗██╔════╝██╔═══██╗██╔══██╗████╗  ██║
    ███████║██║     ██║   ██║██████╔╝██╔██╗ ██║
    ██╔══██║██║     ██║   ██║██╔══██╗██║╚██╗██║
    ██║  ██║╚██████╗╚██████╔╝██║  ██║██║ ╚████║
    ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝"""

    LOGO_MINI = ' 🌰 acorn'

    def __init__(self, conn, session_id, user, theme_name, cwd, is_continue=False, **kwargs):
        super().__init__(**kwargs)
        self.conn = conn
        self.session_id = session_id
        self.user = user
        self.theme_data = get_theme(theme_name)
        self.cwd = cwd
        self.plan_mode = False
        self.context_sent = False
        self._is_continue = is_continue
        self.generating = False
        self._stream_buffer = ''
        self._last_ctrl_c = 0
        self._response_text = []
        self._tool_lines = []
        self._message_count = 0
        self._header_collapsed = False
        self._current_activity = ''
        self._queued_message = None
        self._answering_questions = False
        self._pending_questions = []
        self._pending_answers = {}
        self._pending_notes = {}
        self._current_question_idx = 0
        self._awaiting_plan_decision = False
        self._awaiting_plan_feedback = False
        self._last_plan_text = ''
        self.process_manager = ProcessManager()

    def compose(self) -> ComposeResult:
        yield Static('', id='header-bar')
        yield VerticalScroll(
            RichLog(id='transcript', wrap=True, highlight=True, markup=True),
            Static('', id='stream-area'),
            id='main-scroll',
        )
        with Vertical(id='bottom-area'):
            yield Input(placeholder='Message acorn...', id='user-input')
            yield Static('', id='footer-bar')

    def on_mount(self):
        _register_acorn_themes(self)
        self._apply_theme()
        self._update_header()
        self._update_mode_bar()
        ensure_local_dir(self.cwd)

        self.permissions = TuiPermissions(self)
        self.executor = ToolExecutor(self.permissions, None, self.cwd, process_manager=self.process_manager)
        self.conn.tool_executor = self.executor

        self.conn.on('chat:history', self._on_history)
        self.conn.on('chat:delta', self._on_delta)
        self.conn.on('chat:status', self._on_status)
        self.conn.on('chat:done', self._on_done)
        self.conn.on('chat:error', self._on_error)
        self.conn.on('chat:tool', self._on_tool)
        self.conn.on('code:view', self._on_code_view)
        self.conn.on('code:diff', self._on_code_diff)
        self.conn.on('chat:start', self._on_start)

        self.query_one('#user-input', Input).focus()

        # Run environment audit at startup — cached for the session
        from acorn.context import gather_environment, detect_project_type
        env = gather_environment()
        proj_type = detect_project_type(self.cwd)
        t = self.theme_data

        # Show compact env summary in transcript
        env_lines = env.split('\n')
        summary_parts = []
        for line in env_lines:
            if line.startswith('OS:') or line.startswith('CPU:') or line.startswith('RAM:') or line.startswith('GPU:'):
                summary_parts.append(line)
            elif line.strip().startswith('NVIDIA:') or line.strip().startswith('CUDA'):
                summary_parts.append(line)
        if proj_type != 'Unknown':
            summary_parts.append(f'Project: {proj_type}')
        if summary_parts:
            self._log(Text('  ' + '  │  '.join(s.strip() for s in summary_parts), style=t['muted']))
            self._scroll_bottom()

        # Request history for --continue sessions
        if self._is_continue:
            import json
            asyncio.create_task(
                self.conn.send(json.dumps({'type': 'chat:history-request', 'sessionId': self.session_id}))
            )

    _last_click_time = 0

    def on_click(self, event):
        """Double-click focuses the input. Single click allows text selection."""
        now = time.time()
        if now - self._last_click_time < 0.4:
            try:
                self.query_one('#user-input', Input).focus()
            except NoMatches:
                pass
        self._last_click_time = now

    def on_key(self, event):
        """Typing refocuses the input if it lost focus."""
        if event.key in ('up', 'down', 'left', 'right', 'escape', 'tab', 'ctrl+p', 'ctrl+c'):
            return
        try:
            inp = self.query_one('#user-input', Input)
            if not inp.has_focus:
                inp.focus()
        except NoMatches:
            pass

    # ── UI updates ─────────────────────────────────────────────────

    def _apply_theme(self):
        """Apply theme by switching to the registered Textual theme."""
        t = self.theme_data
        theme_name = f'acorn-{t["name"]}'
        try:
            self.theme = theme_name
        except Exception as e:
            pass  # Theme might not be registered yet on first call

    def _update_header(self):
        t = self.theme_data
        proj = project_name(self.cwd)
        branch = get_git_branch(self.cwd)

        try:
            header_widget = self.query_one('#header-bar', Static)
        except NoMatches:
            return

        if self._header_collapsed:
            # Mini status bar — single line with context
            header_widget.remove_class('collapsed')
            header_widget.add_class('collapsed')
            mini = Text()
            mini.append(' 🌰 ', style=f'bold {t["accent"]}')
            mini.append(self.user, style=f'bold {t["prompt_user"]}')
            mini.append(' ⟩ ', style=t.get('muted', 'dim'))
            mini.append(proj, style=t['prompt_project'])
            if branch:
                mini.append(f' ({branch})', style=t['prompt_branch'])
            mini.append('  │  ', style=t.get('muted', 'dim'))
            if self.generating and self._current_activity:
                mini.append(f'● {self._current_activity}', style=t['thinking'])
            elif self.generating:
                mini.append('● thinking...', style=t['thinking'])
            else:
                mini.append(f'{self._message_count} msgs', style=t.get('muted', 'dim'))
                mode = 'plan' if self.plan_mode else 'exec'
                mini.append(f'  │  {mode}', style=t.get('muted', 'dim'))
            header_widget.update(mini)
        else:
            # Full splash logo
            header_widget.remove_class('collapsed')
            logo = Text()
            for line in self.LOGO_FULL.strip('\n').split('\n'):
                logo.append(line + '\n', style=f'bold {t["accent"]}')
            logo.append(f'    {self.user}', style=f'bold {t["prompt_user"]}')
            logo.append(' → ', style=t.get('muted', 'dim'))
            logo.append(proj, style=t['prompt_project'])
            if branch:
                logo.append(f' ({branch})', style=t['prompt_branch'])
            header_widget.update(logo)

    def _collapse_header(self):
        """Collapse the header after first interaction."""
        if not self._header_collapsed:
            self._header_collapsed = True
            self._update_header()

    def _update_footer(self):
        t = self.theme_data
        proj = project_name(self.cwd)
        width = self.size.width or 80

        try:
            footer = self.query_one('#footer-bar', Static)
        except NoMatches:
            return

        # Line 1: mode indicator
        line1 = Text()
        if self.plan_mode:
            line1.append(' PLAN ', style=t['plan_label'])
            line1.append(' research & plan only', style=t.get('muted', 'dim'))
        else:
            line1.append(' EXEC ', style=t['exec_label'])
            line1.append(' full agent mode', style=t.get('muted', 'dim'))

        # Line 2: key hints
        line2 = Text()
        line2.append(' Ctrl+P', style=f'bold {t["accent"]}')
        line2.append(' mode ', style=t.get('muted', 'dim'))
        line2.append(' Esc', style=f'bold {t["accent"]}')
        line2.append(' stop ', style=t.get('muted', 'dim'))
        line2.append(' /help', style=f'bold {t["accent"]}')
        line2.append(' cmds ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+C×2', style=f'bold {t["accent"]}')
        line2.append(' quit', style=t.get('muted', 'dim'))

        # Line 3: session info
        line3 = Text()
        line3.append(f' {self.user}@{proj}', style=t.get('muted', 'dim'))
        if self.generating:
            line3.append('  ● generating...', style=t['thinking'])
            if self._queued_message:
                line3.append('  │  1 queued', style=t.get('warning', 'yellow'))
        bg_count = self.process_manager.running_count
        if bg_count:
            line3.append(f'  │  {bg_count} bg', style=t.get('accent2', t['accent']))

        combined = Text()
        combined.append_text(line1)
        combined.append('\n')
        combined.append_text(line2)
        combined.append('\n')
        combined.append_text(line3)

        footer.update(combined)

    def _update_mode_bar(self):
        """Update the footer bar (replaces old single-line mode bar)."""
        self._update_footer()

    def _log(self, renderable):
        try:
            self.query_one('#transcript', RichLog).write(renderable)
        except NoMatches:
            pass

    def _themed_panel(self, content, title='', border_style=None, **kwargs):
        """Create a Panel styled with theme colors."""
        t = self.theme_data
        if isinstance(content, str):
            content = Text(content, style=t['fg'])
        return Panel(
            content,
            title=title,
            title_align='left',
            border_style=border_style or t['border'],
            style=f'on {t["bg_panel"]}',
            padding=(0, 1),
            **kwargs,
        )

    def _themed_text(self, text, style=None):
        """Create a Text with theme foreground as base."""
        t = self.theme_data
        return Text(text, style=style or t['fg'])

    def _scroll_bottom(self):
        try:
            self.query_one('#main-scroll', VerticalScroll).scroll_end(animate=False)
        except NoMatches:
            pass

    # ── Actions ────────────────────────────────────────────────────

    def action_toggle_plan(self):
        self.plan_mode = not self.plan_mode
        self._update_mode_bar()
        mode = 'plan' if self.plan_mode else 'execute'
        t = self.theme_data
        self._log(Text(f'  Switched to {mode} mode', style=t['muted']))
        self._scroll_bottom()

    def action_quit_check(self):
        now = time.time()
        # If generating → first Ctrl+C stops generation
        if self.generating:
            from acorn.protocol import stop_message
            asyncio.create_task(self.conn.send(stop_message(self.session_id)))
            self.generating = False
            self._current_activity = ''
            self._queued_message = None
            self._log(Text('  ⏹ Stopped', style='dim'))
            self._update_header()
            self._update_footer()
            self._scroll_bottom()
            self._last_ctrl_c = now
            return
        # If idle → double tap to quit
        if now - self._last_ctrl_c < 1.0:
            self.exit()
        else:
            self._last_ctrl_c = now
            self._log(Text('  Press Ctrl+C again to quit', style='dim'))
            self._scroll_bottom()

    def action_stop_generation(self):
        """Esc also stops generation."""
        if self.generating:
            self.action_quit_check()

    # ── Input handling ─────────────────────────────────────────────

    async def on_input_submitted(self, event: Input.Submitted):
        text = event.value.strip()
        if not text:
            return

        inp = self.query_one('#user-input', Input)
        inp.value = ''

        # Slash commands always run immediately
        if text.startswith('/'):
            await self._handle_command(text)
            return

        # If answering inline questions
        if getattr(self, '_answering_questions', False):
            self._handle_question_answer(text)
            return

        # If awaiting plan decision (1/2/3 or feedback text)
        if getattr(self, '_awaiting_plan_decision', False):
            # If we asked for feedback text after picking "2"
            if getattr(self, '_awaiting_plan_feedback', False):
                self._awaiting_plan_feedback = False
                self._awaiting_plan_decision = False
                # Re-enter with the actual feedback
                self._handle_plan_decision(text)
            else:
                self._handle_plan_decision(text)
            return

        # If generating, queue this message and show it as pending
        if self.generating:
            t = self.theme_data
            self._queued_message = text
            self._log(self._themed_panel(
                f'{text}\n[queued — will send when current response finishes]',
                title=f'[bold]{self.user}[/bold] [dim](queued)[/dim]',
                border_style=t.get('muted', 'dim'),
            ))
            self._scroll_bottom()
            self._update_footer()
            return

        await self._send_message(text)

    async def _send_message(self, text):
        """Send a message to the agent."""
        t = self.theme_data
        self._log(self._themed_panel(text, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))

        content = text
        if not self.context_sent:
            ctx = gather_context(self.cwd)
            content = ctx + '\n\n' + text
            self.context_sent = True

        if self.plan_mode:
            content = PLAN_PREFIX + content

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        self._message_count += 1
        self.generating = True
        self._queued_message = None

        if self._message_count >= 1:
            self._collapse_header()

        self._update_footer()
        self._update_header()
        await self.conn.send(chat_message(self.session_id, content, self.user))

    async def _handle_command(self, text):
        parts = text.split(None, 1)
        cmd = parts[0].lower()
        args = parts[1] if len(parts) > 1 else ''
        t = self.theme_data

        if cmd in ('/quit', '/exit'):
            self.exit()
        elif cmd == '/clear':
            from acorn.protocol import clear_message
            await self.conn.send(clear_message(self.session_id))
            self.context_sent = False
            try:
                self.query_one('#transcript', RichLog).clear()
            except NoMatches:
                pass
            self._log(Text('  Session cleared', style='dim'))
        elif cmd == '/plan':
            self.action_toggle_plan()
        elif cmd == '/status':
            info = Table.grid(padding=(0, 2))
            info.add_row(Text('User', style='dim'), Text(self.user, style=t['prompt_user']))
            info.add_row(Text('Session', style='dim'), Text(self.session_id, style='dim'))
            info.add_row(Text('Server', style='dim'), Text(f'{self.conn.host}:{self.conn.port}'))
            info.add_row(Text('Dir', style='dim'), Text(self.cwd))
            info.add_row(Text('Theme', style='dim'), Text(self.theme_data['name']))
            self._log(Panel(info, title='Status', border_style=t['border'], style=f'on {t["bg_panel"]}'))
        elif cmd == '/theme':
            from acorn.themes import list_themes
            available = list_themes()
            if args and args in available:
                self.theme_data = get_theme(args)
                self._apply_theme()
                self._update_mode_bar()
                self._update_header()
                # Save to global config
                try:
                    cfg = load_config() or {}
                    if 'display' not in cfg:
                        cfg['display'] = {}
                    cfg['display']['theme'] = args
                    save_config(cfg)
                except Exception:
                    pass
                self._log(Text(f'  Theme → {args} (saved)', style=self.theme_data['accent']))
            elif args:
                self._log(Text(f'  Unknown theme. Available: {", ".join(available)}', style='red'))
            else:
                self._log(Text(f'  Current: {self.theme_data["name"]}  Available: {", ".join(available)}', style='dim'))
        elif cmd == '/approve-all':
            self.permissions.approve_all = True
            self._log(Text('  ⚡ All tools auto-approved', style='yellow'))
        elif cmd == '/help':
            help_table = Table.grid(padding=(0, 2))
            help_table.add_column(style='bold cyan', min_width=18)
            help_table.add_column(style='dim')
            help_table.add_row('/help', 'Show this help')
            help_table.add_row('/quit', 'Exit Acorn')
            help_table.add_row('/clear', 'Clear session')
            help_table.add_row('/plan', 'Toggle plan mode')
            help_table.add_row('/status', 'Connection info')
            help_table.add_row('/theme [name]', 'Switch theme')
            help_table.add_row('/approve-all', 'Auto-approve tools')
            help_table.add_row('/test [name]', 'Run UI tests')
            help_table.add_row('/bg', 'Background processes')
            help_table.add_row('/bg run <cmd>', 'Run command in background')
            help_table.add_row('/bg <id>', 'View process output')
            help_table.add_row('/bg kill <id>', 'Kill a process')
            help_table.add_row('', '')
            help_table.add_row('Ctrl+C', 'Stop generation (×2 to quit)')
            help_table.add_row('Ctrl+P', 'Toggle plan/execute')
            help_table.add_row('Esc', 'Stop generation')
            self._log(Panel(help_table, title='Commands', border_style=t['accent'], style=f'on {t["bg_panel"]}'))
        else:
            # Check command registry (for /test and other registered commands)
            from acorn.commands.registry import get_command
            handler = get_command(cmd)
            if handler:
                await handler(args, app=self, conn=self.conn, session_id=self.session_id,
                              user=self.user, renderer=None, executor=self.executor, state={})
            else:
                self._log(Text(f'  Unknown: {cmd}', style='red'))
        self._scroll_bottom()

    # ── WebSocket handlers ─────────────────────────────────────────

    async def _on_history(self, msg):
        messages = msg.get('messages', [])
        if not messages:
            return
        t = self.theme_data
        self._log(Rule('Session History', style=t['separator']))
        for m in messages:
            role = m.get('role', 'user')
            text = m.get('text', '')
            if not text.strip():
                continue
            if role == 'user':
                display = text[:300] + '...' if len(text) > 300 else text
                self._log(self._themed_panel(display, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))
            elif role == 'assistant':
                try:
                    content = Markdown(text)
                except Exception:
                    content = Text(text, style=t['fg'])
                self._log(Panel(content, title='[bold]acorn[/bold]', title_align='left',
                                border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1)))
        self._log(Rule(style=t['separator']))
        self._scroll_bottom()

    async def _on_start(self, msg):
        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        try:
            self.query_one('#stream-area', Static).update('')
        except NoMatches:
            pass

    async def _on_delta(self, msg):
        text = msg.get('text', '')
        self._stream_buffer += text
        self._response_text.append(text)
        try:
            stream = self.query_one('#stream-area', Static)
            t = self.theme_data
            try:
                content = Markdown(self._stream_buffer)
            except Exception:
                content = Text(self._stream_buffer, style=t['fg'])
            stream.update(Panel(
                content,
                title='[bold]acorn[/bold]',
                title_align='left',
                border_style=t['accent'],
                style=f'on {t["bg_panel"]}',
                padding=(0, 1),
            ))
            self.query_one('#main-scroll', VerticalScroll).scroll_end(animate=False)
        except NoMatches:
            pass

    async def _on_status(self, msg):
        t = self.theme_data
        status = msg.get('status', '')
        if status == 'thinking_start':
            self._current_activity = 'thinking...'
            self._update_header()
            self._tool_lines.append(('thinking', '● Thinking...'))
            self._update_tool_display()
        elif status == 'thinking_done':
            self._current_activity = ''
            self._update_header()
            self._tool_lines = [(k, v) for k, v in self._tool_lines if k != 'thinking']
            self._update_tool_display()
        elif status == 'tool_exec_start':
            tool = msg.get('tool', '')
            detail = msg.get('detail', '')[:80]
            self._current_activity = f'{tool} {detail[:40]}'
            self._update_header()
            self._tool_lines.append(('tool_start', f'⚙ {tool} {detail}'))
            self._update_tool_display()
        elif status == 'tool_exec_done':
            self._current_activity = ''
            self._update_header()
            parts = []
            if msg.get('durationMs'):
                parts.append(f'{msg["durationMs"]}ms')
            if msg.get('resultChars'):
                parts.append(f'{msg["resultChars"]:,} chars')
            self._tool_lines.append(('tool_done', f'✓ {" · ".join(parts)}'))
            self._update_tool_display()

    def _update_tool_display(self):
        """Render tool activity lines into the transcript."""
        if not self._tool_lines:
            return
        t = self.theme_data
        last_type, last_text = self._tool_lines[-1]
        style_map = {
            'thinking': t['thinking'],
            'tool_start': t['tool_icon'],
            'tool_done': t['tool_done'],
            'read': t['read_icon'],
            'edit': t['edit_icon'],
        }
        style = style_map.get(last_type, 'dim')
        self._log(Text(f'  {last_text}', style=style))
        self._scroll_bottom()

    async def _on_code_view(self, msg):
        t = self.theme_data
        path = msg.get('path', '')
        lines = msg.get('content', '').count('\n') + 1
        is_new = msg.get('isNew', False)
        label = 'new' if is_new else 'read'
        self._tool_lines.append(('read', f'📄 {label} {path} ({lines} lines)'))
        self._update_tool_display()

    async def _on_code_diff(self, msg):
        t = self.theme_data
        path = msg.get('path', '')
        self._tool_lines.append(('edit', f'✏️  edit {path}'))
        self._update_tool_display()

    async def _on_done(self, msg):
        self.generating = False
        self._update_footer()
        self._update_header()
        response = ''.join(self._response_text)
        t = self.theme_data

        # Clear stream area
        try:
            self.query_one('#stream-area', Static).update('')
        except NoMatches:
            pass

        # Write final response as a bordered panel
        if response.strip():
            try:
                content = Markdown(response)
            except Exception:
                content = Text(response, style=t['fg'])
            self._log(Panel(
                content,
                title='[bold]acorn[/bold]',
                title_align='left',
                border_style=t['accent'],
                style=f'on {t["bg_panel"]}',
                padding=(0, 1),
            ))

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

        self._scroll_bottom()

        # Detect structured questions from the agent
        questions = parse_questions(response) if response else []
        if questions and len(questions) >= 1:
            self._log(Text(f'  Agent has {len(questions)} question(s) for you', style=t['accent2']))
            self._scroll_bottom()
            # Show questions inline and enter question-answering mode
            self._pending_questions = questions
            self._pending_answers = {}
            self._pending_notes = {}
            self._current_question_idx = 0
            self._log(Text(''))
            self._show_current_question()
        elif self.plan_mode and response and ('PLAN_READY' in response or len(response) > 500):
            self._last_plan_text = response
            self._awaiting_plan_decision = True
            self._show_plan_choices()

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []

        # Send queued message if one was waiting
        if self._queued_message and not questions:
            queued = self._queued_message
            self._queued_message = None
            asyncio.create_task(self._send_message(queued))

    def _show_current_question(self):
        """Show the current question inline in the transcript."""
        questions = getattr(self, '_pending_questions', [])
        idx = getattr(self, '_current_question_idx', 0)
        if idx >= len(questions):
            # All questions answered — send answers back
            self._send_question_answers()
            return
        q = questions[idx]
        t = self.theme_data
        total = len(questions)

        header = Text()
        header.append(f'  Question {idx + 1}/{total}: ', style=f'bold {t["accent"]}')
        header.append(q['text'], style='bold')
        self._log(header)

        if q['options']:
            for i, opt in enumerate(q['options']):
                self._log(Text(f'    {i + 1}. {opt}', style=t['fg']))
            if q.get('multi'):
                self._log(Text('  Type numbers separated by commas (e.g. 1,3,4)', style=t['muted']))
            else:
                self._log(Text('  Type a number or your own answer', style=t['muted']))
        else:
            self._log(Text('  Type your answer', style=t['muted']))

        self._scroll_bottom()
        # Set flag so on_input_submitted knows to handle as question answer
        self._answering_questions = True

    def _handle_question_answer(self, text):
        """Process an answer to the current inline question."""
        questions = self._pending_questions
        idx = self._current_question_idx
        q = questions[idx]

        if q['options'] and q.get('multi'):
            # Multi-select: parse comma-separated numbers
            try:
                indices = [int(x.strip()) - 1 for x in text.split(',')]
                selected = [q['options'][i] for i in indices if 0 <= i < len(q['options'])]
                self._pending_answers[idx] = selected if selected else [text]
            except (ValueError, IndexError):
                self._pending_answers[idx] = [text]
        elif q['options'] and text.isdigit():
            # Single select by number
            num = int(text) - 1
            if 0 <= num < len(q['options']):
                self._pending_answers[idx] = q['options'][num]
            else:
                self._pending_answers[idx] = text
        else:
            self._pending_answers[idx] = text

        t = self.theme_data
        answer = self._pending_answers[idx]
        display = ', '.join(answer) if isinstance(answer, list) else str(answer)
        self._log(Text(f'  → {display}', style=t['success']))

        self._current_question_idx += 1
        self._show_current_question()

    def _send_question_answers(self):
        """Format and send all answers back to the agent."""
        self._answering_questions = False
        questions = self._pending_questions
        answers_data = {'answers': self._pending_answers, 'notes': self._pending_notes}
        formatted = format_answers(questions, answers_data)
        t = self.theme_data

        self._log(Text(''))
        self._log(self._themed_panel(formatted, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))
        self._scroll_bottom()

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        self.generating = True
        self._update_footer()
        self._update_header()
        asyncio.create_task(
            self.conn.send(chat_message(self.session_id, formatted, self.user))
        )

    def _show_plan_choices(self):
        """Show plan approval options inline."""
        t = self.theme_data
        self._log(Text(''))
        self._log(Panel(
            Text.assemble(
                ('  1. ', f'bold {t["accent"]}'), ('▶ Execute plan\n', t['success']),
                ('  2. ', f'bold {t["accent"]}'), ('✎ Revise with feedback\n', t['fg']),
                ('  3. ', f'bold {t["accent"]}'), ('✕ Cancel\n', t['muted']),
            ),
            title='[bold]Plan Ready[/bold]',
            border_style=t['accent'],
            style=f'on {t["bg_panel"]}',
            padding=(0, 1),
        ))
        self._log(Text('  Type 1, 2, or 3 (or type feedback directly)', style=t['muted']))
        self._scroll_bottom()

    def _handle_plan_decision(self, text):
        """Handle user input when awaiting plan decision."""
        self._awaiting_plan_decision = False
        t = self.theme_data

        if text == '1' or text.lower().startswith('exec'):
            # Execute the plan
            from acorn.cli import _save_plan
            plan_path = _save_plan(self.cwd, getattr(self, '_last_plan_text', ''))
            if plan_path:
                self._log(self._themed_text(f'  Plan saved to {plan_path}', style=t['muted']))

            self.plan_mode = False
            self._update_mode_bar()
            self._update_header()
            self._log(self._themed_text('  ▶ Executing plan...', style=t['success']))
            self._scroll_bottom()

            self._stream_buffer = ''
            self._response_text = []
            self._tool_lines = []
            self.generating = True
            self._update_footer()
            self._update_header()
            asyncio.create_task(
                self.conn.send(chat_message(self.session_id, PLAN_EXECUTE_MSG, self.user))
            )

        elif text == '3' or text.lower().startswith('cancel'):
            self._log(self._themed_text('  Plan discarded', style=t['muted']))
            self._scroll_bottom()

        else:
            # Anything else (including "2" or free text) is feedback
            feedback = text if text != '2' else ''
            if text == '2':
                # Just picked "revise" — ask for the actual feedback
                self._log(Text('  Type your feedback:', style=t['muted']))
                self._awaiting_plan_decision = True  # stay in this mode for next input
                self._awaiting_plan_feedback = True
                self._scroll_bottom()
                return

            self._log(self._themed_panel(
                feedback,
                title=f'[bold]{self.user}[/bold] [dim](feedback)[/dim]',
                border_style=t['prompt_user'],
            ))
            self._scroll_bottom()

            feedback_msg = f'[PLAN FEEDBACK: Revise the plan based on this feedback. Stay in plan mode.]\n\n{feedback}'
            self._stream_buffer = ''
            self._response_text = []
            self._tool_lines = []
            self.generating = True
            self._update_footer()
            self._update_header()
            asyncio.create_task(
                self.conn.send(chat_message(self.session_id, feedback_msg, self.user))
            )

    async def _on_error(self, msg):
        self.generating = False
        self._update_footer()
        t = self.theme_data
        error = msg.get('error', 'Unknown error')
        self._log(Panel(
            Text(error, style=t['error']),
            title='[bold]Error[/bold]',
            border_style='red',
            style=f'on {t["bg_panel"]}',
            padding=(0, 1),
        ))
        self._scroll_bottom()

    async def _on_tool(self, msg):
        pass


class TuiPermissions:
    """Permissions that work with the TUI."""
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
        summary = tool_name
        if tool_name == 'exec':
            summary = f'exec: {input.get("command", "")[:80]}'
        elif tool_name in ('write_file', 'edit_file'):
            summary = f'{tool_name}: {input.get("path", "")}'
        t = self.app.theme_data
        self.app._log(Text(f'  ⚡ Auto-approved: {summary}', style=t['warning']))
        self.app._scroll_bottom()
        return True
