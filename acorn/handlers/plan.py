"""Plan approval handler mixin — execute, revise, or cancel plans."""

import asyncio
from rich.text import Text
from textual.css.query import NoMatches

from acorn.constants import PLAN_EXECUTE_MSG
from acorn.protocol import chat_message


class PlanMixin:
    """Mixin providing plan approval handling for AcornApp."""

    def _show_plan_choices(self):
        """Show plan approval using the question selector UI."""
        t = self.theme_data
        self._log(Text(''))

        # Use the question selector for plan approval
        self._pending_questions = [{
            'text': 'Plan ready — what would you like to do?',
            'options': ['▶ Execute plan', '✎ Revise with feedback', '✕ Cancel'],
            'multi': False,
            'index': 1,
        }]
        self._pending_answers = {}
        self._pending_notes = {}
        self._current_question_idx = 0
        self._answering_questions = True
        self._q_plan_approval = True
        self._q_open_ended = False
        self._q_selected = 0
        self._q_checked = set()
        self._q_noting = False
        self._q_transitioning = False

        self._hide_widget('#user-input')
        self._show_widget('#question-selector')
        self._render_question_selector()
        try:
            from acorn.ui.widgets import FocusableStatic
            self.query_one('#question-selector', FocusableStatic).focus()
        except (NoMatches, Exception):
            pass

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
            feedback = text if text != '2' else ''
            if text == '2':
                self._log(Text('  Type your feedback:', style=t['muted']))
                self._awaiting_plan_decision = True
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
