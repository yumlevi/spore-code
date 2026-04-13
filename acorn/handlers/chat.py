"""Chat handler — owns chat state, message sending, context enrichment."""

import asyncio
from dataclasses import dataclass
from rich.text import Text

from acorn.constants import PLAN_PREFIX
from acorn.protocol import chat_message


@dataclass
class ChatState:
    queued_message: str = None
    message_count: int = 0


class ChatHandler:
    """Handles message sending. Owns chat state."""

    def __init__(self, bridge):
        self.bridge = bridge
        self.state = ChatState()

    async def handle_submit(self, text):
        """Handle submission from the message input."""
        b = self.bridge
        app = b._app

        b._app._autocomplete_matches = []
        b.hide_widget('#autocomplete')
        b.hide_widget('#paste-indicator')

        if text.startswith('/'):
            await app._handle_command(text)
            return

        # Questions open-ended answer
        if b.sm.state == b.AppState.QUESTIONS and app.questions_handler.state.open_ended:
            app.questions_handler.state.open_ended = False
            app.questions_handler.handle_text_answer(text)
            return

        # Plan feedback
        if b.sm.state in (b.AppState.PLAN_REVIEW, b.AppState.PLAN_FEEDBACK):
            if b.sm.state == b.AppState.PLAN_FEEDBACK:
                app.plan_handler.state.awaiting_feedback = False
                app.plan_handler.state.awaiting_decision = False
                b.sm.transition(b.AppState.IDLE)
            app.plan_handler.handle_decision(text)
            return

        # Queued while generating
        if b.generating:
            t = b.theme
            self.state.queued_message = text
            b.log(b.themed_panel(
                f'{text}\n[queued — will send when current response finishes]',
                title=f'[bold]{b.user}[/bold] [dim](queued)[/dim]',
                border_style=t.get('muted', 'dim'),
            ))
            b.scroll_bottom()
            b.update_footer()
            return

        await self.send_message(text)

    async def send_message(self, text):
        """Send a message to the agent."""
        b = self.bridge
        b.slog.info('send', f'sending {len(text)} chars', plan_mode=b.plan_mode)
        b.session_writer.write_user(text)
        b.log_user_panel(text)

        # Smart context
        ctx = b.ctx_manager.get_context()
        content = (ctx + '\n\n' + text) if ctx else text

        if b.plan_mode:
            content = PLAN_PREFIX + content

        self.state.message_count += 1
        b.generating = True
        self.state.queued_message = None

        if self.state.message_count >= 1:
            b.collapse_header()

        b.update_footer()
        b.update_header()
        await b.conn.send(chat_message(b.session_id, content, b.user))
