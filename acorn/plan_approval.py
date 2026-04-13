"""Plan approval screen — shown after PLAN_READY."""

from textual.app import ComposeResult
from textual.containers import Vertical
from textual.widgets import Static, Input
from textual.binding import Binding
from textual.screen import ModalScreen
from textual.css.query import NoMatches

from rich.text import Text


class PlanApprovalScreen(ModalScreen):
    """Modal for approving, revising, or cancelling a plan.

    Returns:
    - ('execute', None) — run the plan
    - ('feedback', 'user text') — revise with feedback
    - ('cancel', None) — discard
    """

    BINDINGS = [
        Binding('escape', 'cancel', 'Cancel'),
    ]

    CSS = """
    PlanApprovalScreen {
        align: center middle;
    }
    #plan-container {
        width: 70%;
        max-width: 80;
        height: auto;
        max-height: 60%;
        background: $surface;
        border: round $accent;
        padding: 1 2;
    }
    #plan-title {
        text-style: bold;
        margin-bottom: 1;
    }
    #plan-options {
        height: auto;
        margin-bottom: 1;
    }
    #plan-feedback-input {
        margin-top: 1;
    }
    #plan-hint {
        margin-top: 1;
    }
    """

    def __init__(self, theme_data: dict, plan_text: str = '', **kwargs):
        super().__init__(**kwargs)
        self.theme_data = theme_data
        self.plan_text = plan_text
        self.selected = 0
        self._feedback_mode = False
        self.choices = [
            ('execute', '▶  Execute plan', 'Switch to execute mode and run the plan now'),
            ('feedback', '✎  Revise plan', 'Add feedback — agent will update the plan'),
            ('cancel', '✕  Cancel', 'Discard this plan'),
        ]

    def compose(self) -> ComposeResult:
        with Vertical(id='plan-container'):
            yield Static('', id='plan-title')
            yield Static('', id='plan-options')
            yield Input(placeholder='Enter your feedback...', id='plan-feedback-input')
            yield Static('', id='plan-hint')

    def on_mount(self):
        try:
            self.query_one('#plan-feedback-input', Input).display = False
        except NoMatches:
            pass
        self._render()

    def _render(self):
        t = self.theme_data

        title = Text()
        title.append(' Plan Ready ', style=f'bold {t["accent"]}')

        try:
            self.query_one('#plan-title', Static).update(title)
        except NoMatches:
            pass

        if self._feedback_mode:
            try:
                self.query_one('#plan-options', Static).update(
                    Text('Type your feedback and press Enter:', style='bold')
                )
                inp = self.query_one('#plan-feedback-input', Input)
                inp.display = True
                inp.value = ''
                inp.focus()
                self.query_one('#plan-hint', Static).update(
                    Text('Enter to submit · Esc to go back', style='dim')
                )
            except NoMatches:
                pass
            return

        # Show choices
        lines = Text()
        for i, (key, label, desc) in enumerate(self.choices):
            if i == self.selected:
                lines.append(f'  ▸ {label}', style=f'bold {t["accent"]}')
                lines.append(f'  {desc}', style=t.get('muted', 'dim'))
            else:
                lines.append(f'    {label}', style='')
                lines.append(f'  {desc}', style=t.get('muted', 'dim'))
            lines.append('\n')

        try:
            self.query_one('#plan-options', Static).update(lines)
            self.query_one('#plan-feedback-input', Input).display = False
            self.query_one('#plan-hint', Static).update(
                Text('↑↓ select · Enter confirm · Esc cancel', style='dim')
            )
        except NoMatches:
            pass

    def on_key(self, event):
        if self._feedback_mode:
            if event.key == 'escape':
                self._feedback_mode = False
                self._render()
                event.prevent_default()
                event.stop()
            return

        if event.key == 'up':
            self.selected = (self.selected - 1) % len(self.choices)
            self._render()
            event.prevent_default()
        elif event.key == 'down':
            self.selected = (self.selected + 1) % len(self.choices)
            self._render()
            event.prevent_default()
        elif event.key == 'enter':
            choice = self.choices[self.selected][0]
            if choice == 'feedback':
                self._feedback_mode = True
                self._render()
            elif choice == 'execute':
                self.dismiss(('execute', None))
            elif choice == 'cancel':
                self.dismiss(('cancel', None))
            event.prevent_default()

    async def on_input_submitted(self, event: Input.Submitted):
        text = event.value.strip()
        if text:
            self.dismiss(('feedback', text))
        else:
            self._feedback_mode = False
            self._render()

    def action_cancel(self):
        if self._feedback_mode:
            self._feedback_mode = False
            self._render()
        else:
            self.dismiss(('cancel', None))
