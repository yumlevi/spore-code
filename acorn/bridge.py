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

    # ── Timer ──────────────────────────────────────────────────────

    def set_timer(self, delay, callback):
        return self._app.set_timer(delay, callback)
