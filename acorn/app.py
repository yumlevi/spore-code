"""Acorn TUI — full-screen terminal app with pinned header/footer."""

import asyncio
import os
import time

# Enable truecolor if terminal doesn't advertise it.
# Can be disabled via ACORN_NO_TRUECOLOR=1 for terminals that don't support it.
if not os.environ.get("COLORTERM") and not os.environ.get("ACORN_NO_TRUECOLOR"):
    os.environ["COLORTERM"] = "truecolor"

from textual.app import App, ComposeResult
from textual.containers import Vertical, VerticalScroll
from textual.widgets import Static, Input, RichLog, TextArea
from textual.binding import Binding
from textual.css.query import NoMatches

from rich.text import Text
from rich.markdown import Markdown
from rich.panel import Panel
from rich.rule import Rule
from rich.table import Table

from acorn.config import save_last_session, ensure_local_dir, load_config, save_config
from acorn.connection import Connection, AuthError
from acorn.context import gather_context, ContextManager
from acorn.permissions import TuiPermissions
from acorn.protocol import chat_message
from acorn.session import compute_session_id, project_name, get_git_branch
from acorn.tools.executor import ToolExecutor
from acorn.themes import get_theme
from acorn.questions import parse_questions, format_answers
from acorn.background import ProcessManager
from acorn.logging import SessionLogger, cleanup_old_logs
from acorn.prompt import PromptProvider
from acorn.session_writer import SessionWriter, cleanup_old_sessions
from acorn.constants import PLAN_PREFIX, PLAN_EXECUTE_MSG, LOGO_FULL, LOGO_MINI, SLASH_COMMANDS
from acorn.ui.widgets import MessageInput, FocusableStatic, SelectableLog
from acorn.ui.panels import themed_panel, themed_text, user_panel, bot_panel, error_panel
import acorn.commands.test  # noqa: F401 — registers /test command
import acorn.commands.bg    # noqa: F401 — registers /bg command

    # Constants moved to acorn/constants.py


    # Widgets moved to acorn/ui/widgets.py


def _to_hex(color_str):
    """Extract #rrggbb hex from a Rich style string like 'bold #f38ba8'.
    Returns None for named colors (Textual handles those separately)."""
    if not color_str:
        return None
    # Direct hex
    if color_str.startswith('#') and len(color_str) == 7:
        return color_str
    # Hex embedded in style string (e.g. 'bold #f38ba8')
    for part in color_str.split():
        if part.startswith('#') and len(part) == 7:
            return part
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


from acorn.handlers.ws_events import WSEventsMixin
from acorn.handlers.questions import QuestionsMixin
from acorn.handlers.plan import PlanMixin
from acorn.handlers.chat import ChatMixin


class AcornApp(WSEventsMixin, QuestionsMixin, PlanMixin, ChatMixin, App):
    """Full-screen Acorn TUI."""

    BINDINGS = [
        Binding('ctrl+c', 'quit_check', 'Quit', show=False),
        Binding('ctrl+p', 'toggle_plan', 'Plan Mode', show=True, priority=True),
        Binding('ctrl+b', 'show_bg', 'Bg Procs', show=True, priority=True),
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
    #transcript {
        height: 1fr;
        padding: 0 1;
        background: $background;
        color: $foreground;
    }
    #bottom-area {
        dock: bottom;
        height: auto;
        max-height: 8;
        background: $surface;
        border-top: solid $accent;
    }
    #user-input {
        height: 4;
        background: $surface;
        color: $foreground;
        border-top: solid $accent;
    }
    #user-input.hidden {
        display: none;
    }
    #paste-indicator {
        height: 1;
        background: $surface;
        color: $accent;
        padding: 0 1;
    }
    #paste-indicator.hidden {
        display: none;
    }
    TextArea {
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
    #autocomplete {
        height: auto;
        max-height: 10;
        padding: 0 1;
        background: $surface;
        border-top: solid $accent;
    }
    #autocomplete.hidden {
        display: none;
    }
    #question-selector {
        height: auto;
        max-height: 12;
        padding: 0 1;
        background: $surface;
    }
    #question-selector.hidden {
        display: none;
    }
    #note-input {
        height: 3;
        padding: 0 1;
        background: $surface;
        color: $foreground;
    }
    #note-input.hidden {
        display: none;
    }
    #user-input.hidden {
        display: none;
    }
    """


    def __init__(self, conn, session_id, user, theme_name, cwd, is_continue=False, **kwargs):
        super().__init__(**kwargs)
        self.conn = conn
        self.session_id = session_id
        self.user = user
        self.theme_data = get_theme(theme_name)
        self.cwd = cwd
        self.plan_mode = False
        self.context_sent = False  # legacy flag, kept for compat
        self.ctx_manager = ContextManager(cwd)
        self._is_continue = is_continue
        self._generating = False

        # State machine — tracks overall app state
        from acorn.state import StateMachine, AppState
        self.sm = StateMachine()
        self._AppState = AppState
        self.sm.on_change(lambda old, new: self.slog.debug('state', f'{old.name} → {new.name}') if hasattr(self, 'slog') else None)
        self._stream_buffer = ''
        self._last_ctrl_c = 0
        self._response_text = []
        self._tool_lines = []
        self._message_count = 0
        self._header_collapsed = False
        self._current_activity = ''
        self._queued_message = None
        self._spinner_frame = 0
        self._spinner_timer = None
        self._answering_questions = False
        self._pending_questions = []
        self._pending_answers = {}
        self._pending_notes = {}
        self._current_question_idx = 0
        self._awaiting_plan_decision = False
        self._awaiting_plan_feedback = False
        self._last_plan_text = ''
        self.process_manager = ProcessManager()
        self.prompter = PromptProvider(self)
        import atexit
        atexit.register(lambda: self.process_manager.kill_all())
        self._session_start = __import__('time').time()
        self.slog = SessionLogger(session_id, user, cwd)
        self.slog.info('init', f'AcornApp created theme={theme_name} continue={is_continue}')
        self.session_writer = SessionWriter(session_id)
        cleanup_old_logs(keep_days=14)
        cleanup_old_sessions(keep_days=30)
        self._autocomplete_selected = 0
        self._autocomplete_matches = []
        self._slash_commands = SLASH_COMMANDS

    def compose(self) -> ComposeResult:
        yield Static('', id='header-bar')
        yield SelectableLog(id='transcript', wrap=True, highlight=True, markup=True)
        with Vertical(id='bottom-area'):
            yield Static('', id='autocomplete', classes='hidden')
            yield Static('', id='paste-indicator', classes='hidden')
            yield MessageInput('', id='user-input', language=None, show_line_numbers=False, soft_wrap=True)
            yield FocusableStatic('', id='question-selector', classes='hidden')
            yield Input(placeholder='Add context/notes (Tab to go back)...', id='note-input', classes='hidden')
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
        self.conn._slog = self.slog
        self.conn._session_writer = self.session_writer
        self.conn._on_disconnect = lambda: self._on_ws_disconnect()
        self.conn._on_reconnect = lambda: self._on_ws_reconnect()

        self.conn.on('chat:history', self._on_history)
        self.conn.on('chat:delta', self._on_delta)
        self.conn.on('chat:status', self._on_status)
        self.conn.on('chat:done', self._on_done)
        self.conn.on('chat:error', self._on_error)
        self.conn.on('chat:tool', self._on_tool)
        self.conn.on('code:view', self._on_code_view)
        self.conn.on('code:diff', self._on_code_diff)
        self.conn.on('chat:start', self._on_start)

        self.query_one('#user-input', MessageInput).focus()

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

        # Resume previous session — try local JSONL first, then server
        if self._is_continue:
            from acorn.session_writer import load_session
            local_history = load_session(self.session_id)
            if local_history:
                self._render_local_history(local_history)
            else:
                import json
                asyncio.create_task(
                    self.conn.send(json.dumps({'type': 'chat:history-request', 'sessionId': self.session_id}))
                )

    def _render_local_history(self, messages):
        """Render chat history from local JSONL file."""
        t = self.theme_data
        user_count = sum(1 for m in messages if m.get('role') == 'user')
        assistant_count = sum(1 for m in messages if m.get('role') == 'assistant')
        self._log(Rule(f'Local history ({user_count} sent, {assistant_count} received)', style=t['separator']))
        for m in messages:
            role = m.get('role', '')
            text = m.get('text', '')
            if not text or not text.strip():
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
            elif role == 'error':
                self._log(Text(f'  ✗ {text}', style=t['error']))
            # Skip tool entries — too verbose for history
        self._log(Rule(style=t['separator']))
        self._log(Text(f'  Context will be re-sent on next message', style=t['muted']))
        self.ctx_manager.reset()  # Force full context on next message
        self._scroll_bottom()

    def on_message_input_submitted(self, event):
        """Handle Enter from the MessageInput widget."""
        self._autocomplete_matches = []
        self._hide_widget('#autocomplete')
        self._hide_widget('#paste-indicator')
        asyncio.create_task(self._handle_textarea_submit(event.text))

    def on_key(self, event):
        """Route keys: question selector, autocomplete."""
        # Question selector mode — intercept arrow keys, space, tab, enter, escape
        if self.sm.state == self._AppState.QUESTIONS and not getattr(self, '_q_open_ended', False) and not getattr(self, '_q_noting', False):
            q = self._pending_questions[self._current_question_idx] if self._current_question_idx < len(self._pending_questions) else None
            if q and q.get('options'):
                if event.key in ('up', 'down', 'space', 'tab', 'enter', 'escape'):
                    self._handle_question_key(event.key)
                    event.prevent_default()
                    event.stop()
                    return

        # Note input escape → back to selector
        if getattr(self, '_q_noting', False) and event.key == 'escape':
            self._q_noting = False
            try:
                note_val = self.query_one('#note-input', Input).value.strip()
                if note_val:
                    self._pending_notes[self._current_question_idx] = note_val
                self._hide_widget('#note-input')
                self._show_widget('#question-selector')
                self._render_question_selector()
                self.query_one('#question-selector', FocusableStatic).focus()
            except NoMatches:
                pass
            event.prevent_default()
            event.stop()
            return

        # Default: refocus input on typing
        if event.key in ('up', 'down', 'left', 'right', 'escape', 'tab', 'ctrl+p', 'ctrl+c'):
            return
        try:
            inp = self.query_one('#user-input', MessageInput)
            if not inp.has_focus and inp.display:
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
            SPINNER = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
            mini.append('  │  ', style=t.get('muted', 'dim'))
            if self.generating:
                frame = SPINNER[self._spinner_frame % len(SPINNER)]
                activity = self._current_activity or 'thinking...'
                mini.append(f'{frame} {activity}', style=t['thinking'])
            else:
                mini.append(f'{self._message_count} msgs', style=t.get('muted', 'dim'))
                mode = 'plan' if self.plan_mode else 'exec'
                mini.append(f'  │  {mode}', style=t.get('muted', 'dim'))
            header_widget.update(mini)
        else:
            # Full splash logo
            header_widget.remove_class('collapsed')
            logo = Text()
            for line in LOGO_FULL.strip('\n').split('\n'):
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
        line2.append(' ↑↓', style=f'bold {t["accent"]}')
        line2.append(' history ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+S', style=f'bold {t["accent"]}')
        line2.append(' stash ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+R', style=f'bold {t["accent"]}')
        line2.append(' pop ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+P', style=f'bold {t["accent"]}')
        line2.append(' plan ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+B', style=f'bold {t["accent"]}')
        line2.append(' bg ', style=t.get('muted', 'dim'))
        line2.append(' Ctrl+C', style=f'bold {t["accent"]}')
        line2.append(' stop', style=t.get('muted', 'dim'))

        # Line 3: session info + animated status
        SPINNER = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
        line3 = Text()
        line3.append(f' {self.user}@{proj}', style=t.get('muted', 'dim'))
        if self.generating:
            frame = SPINNER[self._spinner_frame % len(SPINNER)]
            activity = self._current_activity or 'generating'
            line3.append(f'  {frame} {activity}', style=t['thinking'])
            if self._queued_message:
                line3.append('  │  1 queued', style=t.get('warning', 'yellow'))
        if not self.generating and hasattr(self, 'permissions'):
            perm_mode = getattr(self.permissions, 'mode', 'ask')
            mode_icons = {'auto': '⚡', 'ask': '🔒', 'locked': '🚫'}
            line3.append(f'  │  {mode_icons.get(perm_mode, "")} {perm_mode}', style=t.get('muted', 'dim'))
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

    @property
    def generating(self):
        return self._generating

    @generating.setter
    def generating(self, value):
        was = self._generating
        self._generating = value
        # Sync state machine
        if value and not was:
            self.sm.transition(self._AppState.GENERATING)
            self._start_spinner()
        elif not value and was:
            if self.sm.state in (self._AppState.GENERATING, self._AppState.STREAMING, self._AppState.TOOL_PENDING):
                self.sm.transition(self._AppState.IDLE)
            self._stop_spinner()

    def _start_spinner(self):
        """Start the animated spinner in the footer."""
        if self._spinner_timer:
            return
        self._spinner_frame = 0
        def _tick():
            self._spinner_frame += 1
            self._update_footer()
            # Also update header activity indicator
            self._update_header()
        self._spinner_timer = self.set_interval(0.1, _tick)

    def _stop_spinner(self):
        """Stop the spinner animation."""
        if self._spinner_timer:
            self._spinner_timer.stop()
            self._spinner_timer = None
        self._update_footer()
        self._update_header()

    def _update_mode_bar(self):
        """Update the footer bar (replaces old single-line mode bar)."""
        self._update_footer()

    def _log(self, renderable):
        try:
            self.query_one('#transcript', SelectableLog).write(renderable)
        except NoMatches:
            pass

    def _themed_panel(self, content, title='', border_style=None, **kwargs):
        return themed_panel(self.theme_data, content, title, border_style, **kwargs)

    def _themed_text(self, text, style=None):
        return themed_text(self.theme_data, text, style)

    def _scroll_bottom(self):
        try:
            self.query_one('#transcript', SelectableLog).scroll_end(animate=False)
        except NoMatches:
            pass

    # ── Actions ────────────────────────────────────────────────────

    def _on_ws_disconnect(self):
        """Called when WebSocket disconnects."""
        t = self.theme_data
        self._log(Text('  ⚠ Connection lost — reconnecting...', style=t['warning']))
        self._scroll_bottom()
        self.sm.transition(self._AppState.DISCONNECTED)

    def _on_ws_reconnect(self):
        """Called when WebSocket reconnects."""
        t = self.theme_data
        self._log(Text('  ✓ Reconnected', style=t['success']))
        self._scroll_bottom()
        self.sm.transition(self._AppState.IDLE)

    def action_show_bg(self):
        """Show background process output inline."""
        t = self.theme_data
        procs = self.process_manager.list_all()
        if not procs:
            self._log(Text('  No background processes', style=t['muted']))
            self._scroll_bottom()
            return
        for bp in procs:
            status_text = '● running' if bp.running else f'✓ done (exit {bp.exit_code})'
            status_style = t['success'] if bp.running else t['muted']
            if bp.exit_code and bp.exit_code != 0:
                status_style = t['error']
            header = Text()
            header.append(f'#{bp.id} ', style=f'bold {t["accent"]}')
            header.append(bp.command[:60], style=t['fg'])
            header.append(f'  {status_text}', style=status_style)
            header.append(f'  {bp.elapsed}', style=t['muted'])
            self._log(header)
            # Show last 10 lines of output
            if bp.output:
                output_lines = list(bp.output)[-10:]
                for line in output_lines:
                    self._log(Text(f'    {line[:120]}', style=t['muted']))
                if len(bp.output) > 10:
                    self._log(Text(f'    ... ({len(bp.output) - 10} more lines)', style=t['muted']))
            self._log(Text(''))
        self._scroll_bottom()

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
            self.slog.session_end(self._message_count, __import__('time').time() - self._session_start)
            self.slog.close()
            self.session_writer.close()
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

    def on_text_area_changed(self, event: TextArea.Changed):
        """Show autocomplete + paste indicator on TextArea changes."""
        if event.text_area.id != 'user-input':
            return
        text = event.text_area.text

        # Paste indicator — show line count if content exceeds visible area
        line_count = text.count('\n') + 1
        char_count = len(text)
        if line_count > 10 or char_count > 500:
            t = self.theme_data
            try:
                indicator = self.query_one('#paste-indicator', Static)
                indicator.update(Text(
                    f'  📋 {line_count} lines · {char_count:,} chars (Enter to send)',
                    style=t['accent'],
                ))
                self._show_widget('#paste-indicator')
            except NoMatches:
                pass
        else:
            self._hide_widget('#paste-indicator')

        # Autocomplete for slash commands
        first_line = text.split('\n')[0] if text else ''
        if first_line.startswith('/') and '\n' not in text:
            query = first_line.lower()
            matches = [(cmd, desc) for cmd, desc in self._slash_commands if cmd.startswith(query)]
            self._autocomplete_matches = matches
            self._autocomplete_selected = 0
            if matches:
                self._render_autocomplete()
                self._show_widget('#autocomplete')
            else:
                self._hide_widget('#autocomplete')
        else:
            self._autocomplete_matches = []
            self._hide_widget('#autocomplete')

    def _render_autocomplete(self):
        t = self.theme_data
        lines = Text()
        for i, (cmd, desc) in enumerate(self._autocomplete_matches[:8]):
            if i == self._autocomplete_selected:
                lines.append(f' ▸ {cmd}  ', style=f'bold {t["accent"]}')
                lines.append(desc, style=t['fg'])
            else:
                lines.append(f'   {cmd}  ', style=t['fg'])
                lines.append(desc, style=t['muted'])
            lines.append('\n')
        try:
            self.query_one('#autocomplete', Static).update(lines)
        except NoMatches:
            pass

    async def _handle_command(self, text):
        parts = text.split(None, 1)
        cmd = parts[0].lower()
        args = parts[1] if len(parts) > 1 else ''
        t = self.theme_data
        self.slog.command(cmd, args)

        if cmd in ('/quit', '/exit'):
            self.slog.session_end(self._message_count, __import__('time').time() - self._session_start)
            self.slog.close()
            self.session_writer.close()
            self.exit()
        elif cmd == '/sessions':
            from acorn.session_writer import list_project_sessions
            sessions = list_project_sessions(self.user, self.cwd)
            if not sessions:
                self._log(Text('  No saved sessions for this project', style=t['muted']))
            else:
                self._log(Text(f'  {len(sessions)} session(s) for this project:', style=t['accent']))
                for i, s in enumerate(sessions[:15]):
                    current = ' ◂' if s['session_id'] == self.session_id else ''
                    self._log(Text(f'    {i+1}. {s["time_ago"]:12s} {s["message_count"]:3d} msgs  {s["preview"][:50]}{current}', style=t['fg']))
                self._log(Text(f'\n  Use acorn -c to resume (picks from these)', style=t['muted']))
            self._scroll_bottom()
        elif cmd == '/stop':
            self.action_quit_check() if self.generating else self._log(Text('  Nothing to stop', style=t['muted']))
        elif cmd == '/clear':
            from acorn.protocol import clear_message
            await self.conn.send(clear_message(self.session_id))
            self.context_sent = False
            self.ctx_manager.reset()
            try:
                self.query_one('#transcript', SelectableLog).clear()
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
            self.permissions.mode = 'auto'
            self._log(Text('  ⚡ Auto mode — all non-dangerous tools auto-approved', style='yellow'))
        elif cmd == '/mode':
            if args in ('auto', 'ask', 'locked'):
                self.permissions.mode = args
                descs = {'auto': 'auto-approve (dangerous still asks)', 'ask': 'ask for each tool', 'locked': 'deny all writes/exec'}
                self._log(Text(f'  Mode → {args}: {descs[args]}', style=t['accent']))
                if self.permissions.session_rules:
                    self._log(Text(f'  Session rules: {", ".join(sorted(self.permissions.session_rules))}', style=t['muted']))
            elif args == 'rules':
                rules = self.permissions.session_rules
                if rules:
                    for r in sorted(rules):
                        self._log(Text(f'    {r}', style=t['fg']))
                else:
                    self._log(Text('  No session rules', style=t['muted']))
            else:
                self._log(Text(f'  Current: {self.permissions.mode}', style=t['accent']))
                self._log(Text(f'  /mode auto     Auto-approve (dangerous still asks)', style=t['muted']))
                self._log(Text(f'  /mode ask      Prompt for every tool', style=t['muted']))
                self._log(Text(f'  /mode locked   Deny all writes/exec', style=t['muted']))
                self._log(Text(f'  /mode rules    Show session allow rules', style=t['muted']))
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
            help_table.add_row('/mode [auto/ask/locked]', 'Tool approval mode')
            help_table.add_row('/approve-all', 'Shortcut for /mode auto')
            help_table.add_row('/test [name]', 'Run UI tests')
            help_table.add_row('/bg', 'Background processes')
            help_table.add_row('/bg run <cmd>', 'Run command in background')
            help_table.add_row('/bg <id>', 'View process output')
            help_table.add_row('/bg kill <id>', 'Kill a process')
            help_table.add_row('/sessions', 'List saved sessions')
            help_table.add_row('', '')
            help_table.add_row('Ctrl+C', 'Stop generation (×2 to quit)')
            help_table.add_row('Ctrl+P', 'Toggle plan/execute')
            help_table.add_row('Ctrl+B', 'Show background processes')
            help_table.add_row('↑↓', 'Cycle message history')
            help_table.add_row('Ctrl+S', 'Stash current message')
            help_table.add_row('Ctrl+R', 'Pop stashed message')
            help_table.add_row('Ctrl+J', 'Insert newline')
            help_table.add_row('Esc', 'Stop generation')
            self._log(Panel(help_table, title='Commands', border_style=t['accent'], style=f'on {t["bg_panel"]}'))
        else:
            # Check command registry (for /test and other registered commands)
            from acorn.commands.registry import get_command
            handler = get_command(cmd)
            if handler:
                try:
                    await handler(args, app=self, conn=self.conn, session_id=self.session_id,
                                  user=self.user, renderer=None, executor=self.executor, state={})
                except (AttributeError, TypeError) as e:
                    self._log(Text(f'  Command error: {e}', style=t['error']))
            else:
                self._log(Text(f'  Unknown: {cmd}', style='red'))
        self._scroll_bottom()

    # ── WebSocket handlers are in acorn/handlers/ws_events.py (WSEventsMixin) ──

    # TuiPermissions is now in acorn/permissions.py
