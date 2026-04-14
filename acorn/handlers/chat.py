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

        b.clear_autocomplete()
        b.hide_widget('#paste-indicator')

        if text.startswith('/'):
            await b.handle_command(text)
            return

        # Questions open-ended answer
        qh = b.get_questions_handler()
        if b.sm.state == b.AppState.QUESTIONS and qh.state.open_ended:
            qh.state.open_ended = False
            qh.handle_text_answer(text)
            return

        # Plan feedback
        ph = b.get_plan_handler()
        if b.sm.state in (b.AppState.PLAN_REVIEW, b.AppState.PLAN_FEEDBACK):
            if b.sm.state == b.AppState.PLAN_FEEDBACK:
                ph.state.awaiting_feedback = False
                ph.state.awaiting_decision = False
                b.sm.transition(b.AppState.IDLE)
            ph.handle_decision(text)
            return

        # Interjection while generating — send immediately, server injects into running loop
        if b.generating:
            t = b.theme
            b.log(b.themed_panel(
                text,
                title=f'[bold]{b.user}[/bold] [dim](interjecting)[/dim]',
                border_style=t.get('accent2', 'yellow'),
            ))
            b.scroll_bottom()
            await self.send_interjection(text)
            return

        await self.send_message(text)

    async def send_interjection(self, text):
        """Send an interjection while the agent is running. Stays in generating state."""
        b = self.bridge
        b.slog.info('interjection', f'interjecting with {len(text)} chars')
        b.session_writer.write_user(text)

        ctx = b.ctx_manager.get_context()
        content = (ctx + '\n\n' + text) if ctx else text

        if b.plan_mode:
            content = PLAN_PREFIX + content

        self.state.queued_message = None  # clear any stale queue
        await b.conn.send(chat_message(b.session_id, content, b.user))

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
