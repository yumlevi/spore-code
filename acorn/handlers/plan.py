"""Plan handler — owns plan state, communicates via bridge."""

import asyncio
import re
from dataclasses import dataclass
from rich.text import Text
from rich.panel import Panel
from rich.table import Table
from textual.css.query import NoMatches

from acorn.constants import PLAN_EXECUTE_MSG
from acorn.protocol import chat_message


@dataclass
class PlanState:
    last_plan_text: str = ''
    awaiting_decision: bool = False
    awaiting_feedback: bool = False


class PlanHandler:
    """Handles plan approval flow. Owns its own state."""

    def __init__(self, bridge):
        self.bridge = bridge
        self.state = PlanState()

    def show_choices(self):
        b = self.bridge
        t = b.theme
        b.log(Text(''))

        # Show file summary
        if self.state.last_plan_text:
            self._show_file_summary(self.state.last_plan_text)

        # Use questions handler for the selector
        qh = b.get_questions_handler()
        qh.state.plan_approval = True
        qh.start_questions([{
            'text': 'Plan ready — what would you like to do?',
            'options': ['▶ Execute plan', '✎ Revise with feedback', '✕ Cancel'],
            'multi': False,
            'index': 1,
        }])

    def _show_file_summary(self, plan_text):
        b = self.bridge
        t = b.theme
        create_re = re.compile(r'(?:create|new file|write)\s+[`"]?([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)', re.IGNORECASE)
        modify_re = re.compile(r'(?:modify|edit|update|change)\s+[`"]?([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)', re.IGNORECASE)

        creates = set(m.group(1) for m in create_re.finditer(plan_text))
        modifies = set(m.group(1) for m in modify_re.finditer(plan_text))
        noise = {'e.g.', 'i.e.', 'etc.', 'v1.0', 'v2.0', 'PLAN_READY'}
        creates -= noise
        modifies -= noise

        if creates or modifies:
            table = Table.grid(padding=(0, 2))
            table.add_column(style=t.get('muted', 'dim'), min_width=8)
            table.add_column()
            for f in sorted(creates):
                table.add_row(Text('create', style=t['success']), Text(f, style=t['fg']))
            for f in sorted(modifies - creates):
                table.add_row(Text('modify', style=t['edit_icon']), Text(f, style=t['fg']))
            b.log(Panel(table, title='[bold]Files affected[/bold]', border_style=t['accent'],
                         style=f'on {t["bg_panel"]}', padding=(0, 1)))
            b.scroll_bottom()

    def handle_decision(self, text):
        b = self.bridge
        s = self.state
        s.awaiting_decision = False
        b.sm.transition(b.AppState.IDLE)
        t = b.theme

        if text == '1' or text.lower().startswith('exec'):
            from acorn.cli import _save_plan
            plan_path = _save_plan(b.cwd, s.last_plan_text)
            if plan_path:
                b.log(b.themed_text(f'  Plan saved to {plan_path}', style=t['muted']))

            b.plan_mode = False
            b.log(b.themed_text('  Mode → execute', style=t['accent']))
            b.log(b.themed_text('  ▶ Executing plan...', style=t['success']))
            b.scroll_bottom()

            b.generating = True
            # Force both footer and header to reflect EXEC mode
            b.update_footer()
            b.update_mode_bar()
            b.update_header()
            asyncio.create_task(b.conn.send(chat_message(b.session_id, PLAN_EXECUTE_MSG, b.user)))

        elif text == '3' or text.lower().startswith('cancel'):
            b.log(b.themed_text('  Plan discarded', style=t['muted']))
            b.scroll_bottom()

        else:
            feedback = text if text != '2' else ''
            if text == '2':
                b.log(Text('  Type your feedback:', style=t['muted']))
                s.awaiting_decision = True
                s.awaiting_feedback = True
                b.sm.transition(b.AppState.PLAN_FEEDBACK)
                b.scroll_bottom()
                return

            b.log(b.themed_panel(
                feedback,
                title=f'[bold]{b.user}[/bold] [dim](feedback)[/dim]',
                border_style=t['prompt_user'],
            ))
            b.scroll_bottom()

            feedback_msg = f'[PLAN FEEDBACK: Revise the plan. Stay in plan mode.]\n\n{feedback}'
            b.generating = True
            b.update_footer()
            b.update_header()
            asyncio.create_task(b.conn.send(chat_message(b.session_id, feedback_msg, b.user)))
