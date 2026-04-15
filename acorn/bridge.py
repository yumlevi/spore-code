"""AppBridge — defined interface that handlers use to interact with the TUI.

Handlers should ONLY use this bridge, never reach into app internals directly.
This decouples handler logic from Textual widget details.
"""

from rich.text import Text
from rich.panel import Panel
from rich.markdown import Markdown


class AppBridge:
    """Facade over AcornApp — handlers interact with the TUI through this."""

    def __init__(self, app):
        self._app = app

    # ── Display ────────────────────────────────────────────────────

    def log(self, renderable):
        """Write a renderable to the transcript."""
        self._app._log(renderable)

    def scroll_bottom(self):
        self._app._scroll_bottom()

    def log_output(self, renderable):
        """Write to the output detail log (toggled with Ctrl+O)."""
        self._app._log_output(renderable)

    def themed_panel(self, content, title='', border_style=None, **kwargs):
        return self._app._themed_panel(content, title, border_style, **kwargs)

    def themed_text(self, text, style=None):
        return self._app._themed_text(text, style)

    def log_bot_panel(self, text):
        """Render a bot response panel."""
        t = self.theme
        try:
            content = Markdown(text)
        except Exception:
            content = Text(text, style=t['fg'])
        self.log(Panel(content, title='[bold]acorn[/bold]', title_align='left',
                        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1)))

    def log_user_panel(self, text, username=None):
        """Render a user message panel."""
        name = username or self.user
        self.log(self.themed_panel(text, title=f'[bold]{name}[/bold]', border_style=self.theme['prompt_user']))

    def log_error(self, text):
        t = self.theme
        self.log(Panel(Text(text, style=t['error']), title='[bold]Error[/bold]',
                        border_style='red', style=f'on {t["bg_panel"]}', padding=(0, 1)))

    # ── Widget control ─────────────────────────────────────────────

    def hide_widget(self, selector):
        self._app._hide_widget(selector)

    def show_widget(self, selector):
        self._app._show_widget(selector)

    def focus_selector(self):
        """Focus the #question-selector widget."""
        try:
            from acorn.ui.widgets import FocusableStatic
            self._app.query_one('#question-selector', FocusableStatic).focus()
        except Exception:
            pass

    def focus_input(self):
        """Focus the #user-input widget."""
        try:
            from acorn.ui.widgets import MessageInput
            inp = self._app.query_one('#user-input', MessageInput)
            inp.clear()
            inp.focus()
        except Exception:
            pass

    def update_selector(self, renderable):
        """Update the #question-selector content."""
        try:
            from acorn.ui.widgets import FocusableStatic
            self._app.query_one('#question-selector', FocusableStatic).update(renderable)
        except Exception:
            pass

    # ── State (read/write through properties) ──────────────────────

    @property
    def theme(self):
        return self._app.theme_data

    @property
    def user(self):
        return self._app.user

    @property
    def session_id(self):
        return self._app.session_id

    @property
    def cwd(self):
        return self._app.cwd

    @property
    def plan_mode(self):
        return self._app.plan_mode

    @plan_mode.setter
    def plan_mode(self, v):
        self._app.plan_mode = v

    @property
    def generating(self):
        return self._app.generating

    @generating.setter
    def generating(self, v):
        self._app.generating = v

    @property
    def sm(self):
        return self._app.sm

    @property
    def AppState(self):
        return self._app._AppState

    @property
    def slog(self):
        return self._app.slog

    @property
    def session_writer(self):
        return self._app.session_writer

    @property
    def permissions(self):
        return self._app.permissions

    @property
    def prompter(self):
        return self._app.prompter

    @property
    def ctx_manager(self):
        return self._app.ctx_manager

    @property
    def conn(self):
        return self._app.conn

    # ── UI updates ─────────────────────────────────────────────────

    def update_header(self):
        self._app._update_header()

    def update_footer(self):
        self._app._update_footer()

    def update_mode_bar(self):
        self._app._update_mode_bar()

    def set_activity(self, text):
        self._app._current_activity = text
        self._app._update_header()

    def collapse_header(self):
        self._app._collapse_header()

    # ── Handler cross-references ───────────────────────────────────
    # Handlers need to call each other. Instead of b._app.questions_handler,
    # they go through the bridge.

    def get_questions_handler(self):
        return self._app.questions_handler

    def get_plan_handler(self):
        return self._app.plan_handler

    def get_chat_handler(self):
        return self._app.chat_handler

    # ── Legacy widget access ───────────────────────────────────────

    def query_note_input(self):
        """Get the note-input Input widget."""
        from textual.widgets import Input
        return self._app.query_one('#note-input', Input)

    def render_local_history(self, messages):
        if hasattr(self._app, '_render_local_history'):
            self._app._render_local_history(messages)

    async def handle_command(self, text):
        await self._app._handle_command(text)

    # ── Autocomplete ───────────────────────────────────────────────

    def clear_autocomplete(self):
        self._app._autocomplete_matches = []
        self.hide_widget('#autocomplete')

    # ── Permission state (temporary — should be on PromptProvider) ─

    def get_permission_attr(self, name, default=None):
        return getattr(self._app, name, default)

    def set_permission_attr(self, name, value):
        setattr(self._app, name, value)

    # ── Timer ──────────────────────────────────────────────────────

    def set_timer(self, delay, callback):
        return self._app.set_timer(delay, callback)

    # ── Broadcast to observers ────────────────────────────────────

    def broadcast(self, msg_type: str, **kwargs):
        """Send a message to all session observers (companion app)."""
        import json, asyncio
        try:
            payload = json.dumps({'type': msg_type, **kwargs})
            conn = self._app.conn
            if conn and conn.connected:
                asyncio.create_task(conn.send(payload))
        except Exception:
            pass
