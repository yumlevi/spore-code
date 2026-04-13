"""Chat handler mixin — message sending, textarea submit, context enrichment."""

import asyncio
from rich.text import Text

from acorn.constants import PLAN_PREFIX
from acorn.protocol import chat_message


class ChatMixin:
    """Mixin providing message sending for AcornApp."""

    async def _handle_textarea_submit(self, text):
        """Handle submission from the TextArea (Enter key)."""
        self._autocomplete_matches = []
        self._hide_widget('#autocomplete')
        self._hide_widget('#paste-indicator')

        if text.startswith('/'):
            await self._handle_command(text)
            return

        if self.sm.state == self._AppState.QUESTIONS and getattr(self, '_q_open_ended', False):
            self._q_open_ended = False
            self._handle_question_answer(text)
            return

        if self.sm.state in (self._AppState.PLAN_REVIEW, self._AppState.PLAN_FEEDBACK):
            if self.sm.state == self._AppState.PLAN_FEEDBACK:
                self._awaiting_plan_feedback = False
                self._awaiting_plan_decision = False
                self.sm.transition(self._AppState.IDLE)
                self._handle_plan_decision(text)
            else:
                self._handle_plan_decision(text)
            return

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
        self.slog.info('send', f'sending {len(text)} chars', plan_mode=self.plan_mode)
        self.session_writer.write_user(text)
        t = self.theme_data
        self._log(self._themed_panel(text, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))

        # Smart context — full on first message, delta after
        ctx = self.ctx_manager.get_context()
        content = (ctx + '\n\n' + text) if ctx else text
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
