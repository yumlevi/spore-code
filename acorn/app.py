"""Acorn TUI — full-screen terminal app with pinned header/footer."""

import asyncio
import os
import time

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
from acorn.questions import parse_questions, QuestionScreen, format_answers
import acorn.commands.test  # noqa: F401 — registers /test command

PLAN_PREFIX = (
    '[MODE: Plan only. You are in planning mode.\n'
    'Phase 1 — UNDERSTAND: Read files, search the codebase, and use web_search/web_fetch as needed to fully understand the task. '
    'If anything is ambiguous, ask the user clarifying questions using this exact format:\n\n'
    'QUESTIONS:\n'
    '1. Your question here? [Option A / Option B / Option C]\n'
    '2. Open-ended question here?\n\n'
    'Use [brackets with / separated options] when there are clear choices. Omit brackets for open-ended questions. '
    'The client will present these as an interactive form and return the answers.\n'
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


def _register_acorn_themes(app):
    """Register our themes as native Textual themes so backgrounds actually work."""
    from textual.theme import Theme as TextualTheme
    from acorn.themes import THEMES

    for name, t in THEMES.items():
        is_dark = name != 'light'
        app.register_theme(TextualTheme(
            name=f'acorn-{name}',
            primary=t['accent'].lstrip('#') if t['accent'].startswith('#') else t['accent'],
            secondary=t.get('accent2', t['accent']),
            background=t['bg'],
            surface=t['bg_header'],
            panel=t['bg_panel'],
            foreground=t['fg'],
            warning=t.get('warning', '#d29922').replace('bold ', ''),
            error=t.get('error', '#f85149').replace('bold ', ''),
            success=t.get('success', '#3fb950'),
            accent=t['accent'],
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
    #header-bar {
        dock: top;
        height: 3;
        padding: 0 1;
    }
    #main-scroll {
        height: 1fr;
    }
    #transcript {
        height: auto;
        padding: 0 1;
    }
    #stream-area {
        height: auto;
        padding: 0 2;
        margin: 0 1;
    }
    #mode-bar {
        dock: bottom;
        height: 1;
        width: 100%;
    }
    #user-input {
        dock: bottom;
        height: 3;
        padding: 0 1;
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
        self._tool_lines = []

    def compose(self) -> ComposeResult:
        yield Static('', id='header-bar')
        yield VerticalScroll(
            RichLog(id='transcript', wrap=True, highlight=True, markup=True),
            Static('', id='stream-area'),
            id='main-scroll',
        )
        yield Input(placeholder='Message acorn...', id='user-input')
        yield Static('', id='mode-bar')

    def on_mount(self):
        _register_acorn_themes(self)
        self._apply_theme()
        self._update_header()
        self._update_mode_bar()
        ensure_local_dir(self.cwd)

        self.permissions = TuiPermissions(self)
        self.executor = ToolExecutor(self.permissions, None, self.cwd)
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

    def on_click(self, event):
        """Click on non-input areas refocuses the input field.
        Don't steal focus from the input itself or prevent text selection."""
        # Only refocus if the click target isn't the input
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

        # Switch Textual theme — this handles background, surface, panel, etc.
        try:
            self.theme = theme_name
        except Exception:
            pass

        # Extra styling Textual doesn't handle — borders from our theme
        border_color = t['border']
        try:
            self.query_one('#header-bar', Static).styles.border_bottom = ('solid', border_color)
        except NoMatches:
            pass
        try:
            self.query_one('#user-input', Input).styles.border_top = ('solid', border_color)
        except NoMatches:
            pass

    def _update_header(self):
        t = self.theme_data
        proj = project_name(self.cwd)
        branch = get_git_branch(self.cwd)
        icon = t.get('icon', '🌰')

        header = Text()
        header.append(f' {icon} ', style='bold')
        header.append('acorn', style=f'bold {t["accent"]}')
        header.append('  ', style='')
        header.append(self.user, style=f'bold {t["prompt_user"]}')
        header.append('  ', style='dim')
        header.append(proj, style=f'{t["prompt_project"]}')
        if branch:
            header.append(f'  {branch}', style=f'{t["prompt_branch"]}')

        try:
            self.query_one('#header-bar', Static).update(header)
        except NoMatches:
            pass

    def _update_mode_bar(self):
        try:
            bar = self.query_one('#mode-bar', Static)
        except NoMatches:
            return
        width = self.size.width or 80
        t = self.theme_data
        if self.plan_mode:
            pad = ' ' * max(0, width - 42)
            line = Text()
            line.append(' PLAN ', style=t['plan_label'])
            line.append(f' research & plan only  ctrl+p toggle {pad}', style=t['plan_bar_bg'])
            bar.update(line)
        else:
            pad = ' ' * max(0, width - 42)
            line = Text()
            line.append(' EXECUTE ', style=t['exec_label'])
            line.append(f' full agent mode  ctrl+p toggle {pad}', style=t['exec_bar_bg'])
            bar.update(line)

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
            self._log(Text('  ⏹ Stopped', style='dim'))
            self._scroll_bottom()

    # ── Input handling ─────────────────────────────────────────────

    async def on_input_submitted(self, event: Input.Submitted):
        text = event.value.strip()
        if not text:
            return

        inp = self.query_one('#user-input', Input)
        inp.value = ''

        if text.startswith('/'):
            await self._handle_command(text)
            return

        # Show user message in a bordered panel
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
        self.generating = True
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
            help_table.add_row('', '')
            help_table.add_row('Ctrl+P', 'Toggle plan/execute')
            help_table.add_row('Esc', 'Stop generation')
            help_table.add_row('Ctrl+C ×2', 'Quit')
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
            self._tool_lines.append(('thinking', '● Thinking...'))
            self._update_tool_display()
        elif status == 'thinking_done':
            self._tool_lines = [(k, v) for k, v in self._tool_lines if k != 'thinking']
            self._update_tool_display()
        elif status == 'tool_exec_start':
            tool = msg.get('tool', '')
            detail = msg.get('detail', '')[:80]
            self._tool_lines.append(('tool_start', f'⚙ {tool} {detail}'))
            self._update_tool_display()
        elif status == 'tool_exec_done':
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
            # Launch interactive question modal
            self.app_questions = questions
            self.push_screen(
                QuestionScreen(questions, t),
                callback=self._on_questions_answered,
            )
        elif self.plan_mode and response and ('PLAN_READY' in response or len(response) > 500):
            self._log(Panel(
                Text('Type "execute" to run the plan, or provide feedback', style=t['fg']),
                border_style=t['accent2'],
                style=f'on {t["bg_panel"]}',
                padding=(0, 1),
            ))
            self._scroll_bottom()

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []

    def _on_questions_answered(self, answers):
        """Callback when user finishes the question modal."""
        if answers is None:
            self._log(Text('  Questions cancelled', style='dim'))
            self._scroll_bottom()
            return
        # Format and send answers back to the agent
        questions = getattr(self, 'app_questions', [])
        formatted = format_answers(questions, answers)
        t = self.theme_data
        self._log(self._themed_panel(formatted, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))
        self._scroll_bottom()
        # Send to agent
        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        self.generating = True
        asyncio.create_task(
            self.conn.send(chat_message(self.session_id, formatted, self.user))
        )

    async def _on_error(self, msg):
        self.generating = False
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
