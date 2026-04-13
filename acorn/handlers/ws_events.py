"""WebSocket event handlers — mixin for AcornApp.

Handles: chat:history, chat:start, chat:delta, chat:status,
chat:done, chat:error, chat:tool, code:view, code:diff
"""

import asyncio
from rich.text import Text
from rich.panel import Panel
from rich.markdown import Markdown
from rich.rule import Rule

from acorn.questions import parse_questions


class WSEventsMixin:
    """Mixin providing WebSocket event handlers for AcornApp."""

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
        self.slog.debug('ws', 'chat:start')
        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        self._streaming_started = False

    def _flush_stream_buffer(self):
        """Flush accumulated text as a panel — called before tool events and on done."""
        if self._stream_buffer.strip():
            t = self.theme_data
            try:
                content = Markdown(self._stream_buffer)
            except Exception:
                content = Text(self._stream_buffer, style=t['fg'])
            self._log(Panel(
                content,
                title='[bold]acorn[/bold]',
                title_align='left',
                border_style=t['accent'],
                style=f'on {t["bg_panel"]}',
                padding=(0, 1),
            ))
            self._scroll_bottom()
        self._stream_buffer = ''
        self._streaming_started = False

    async def _on_delta(self, msg):
        text = msg.get('text', '')
        self._stream_buffer += text
        self._response_text.append(text)

        if not getattr(self, '_streaming_started', False):
            self._streaming_started = True

    async def _on_status(self, msg):
        t = self.theme_data
        status = msg.get('status', '')
        self.slog.debug('ws:status', status, **{k: v for k, v in msg.items() if k != 'type' and k != 'status'})

        # Flush any accumulated text before tool events
        if status in ('tool_exec_start', 'thinking_start'):
            self._flush_stream_buffer()

        if status == 'thinking_start':
            self._current_activity = 'thinking...'
            self._update_header()
            self._tool_lines.append(('thinking', '● Thinking...'))
            self._update_tool_display()
        elif status == 'thinking_done':
            self._current_activity = ''
            self._update_header()
            self._tool_lines = [(k, v) for k, v in self._tool_lines if k != 'thinking']
            self._update_tool_display()
        elif status == 'tool_exec_start':
            tool = msg.get('tool', '')
            detail = msg.get('detail', '')[:80]
            self._current_activity = f'{tool} {detail[:40]}'
            self._update_header()
            self._tool_lines.append(('tool_start', f'⚙ {tool} {detail}'))
            self._update_tool_display()
        elif status == 'tool_exec_done':
            self._current_activity = ''
            self._update_header()
            parts = []
            if msg.get('durationMs'):
                parts.append(f'{msg["durationMs"]}ms')
            if msg.get('resultChars'):
                parts.append(f'{msg["resultChars"]:,} chars')
            self._tool_lines.append(('tool_done', f'✓ {" · ".join(parts)}'))
            self._update_tool_display()

    def _update_tool_display(self):
        """Render the latest tool activity line into the transcript."""
        if not self._tool_lines:
            return
        t = self.theme_data
        last_type, last_text = self._tool_lines.pop()
        self._tool_lines.clear()
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
        self._update_footer()
        self._update_header()
        response = ''.join(self._response_text)
        usage = msg.get('usage', {})
        server_text = msg.get('text', '')
        self.slog.info('ws:done', f'{len(response)} chars accumulated, {len(server_text)} chars server',
                      iters=msg.get('iterations'), tools=msg.get('toolUsage'),
                      input_tokens=usage.get('input_tokens'), output_tokens=usage.get('output_tokens'))
        if response.strip():
            self._last_response = response
        t = self.theme_data

        # Flush any remaining streamed text
        self._flush_stream_buffer()

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
        self.slog.info('question-detect', f'response={len(response)} chars, questions={len(questions)}',
                       has_marker='QUESTIONS' in response.upper() if response else False)
        if questions and len(questions) >= 1:
            self._log(Text(f'  Agent has {len(questions)} question(s) for you', style=t['accent2']))
            self._scroll_bottom()
            self._pending_questions = questions
            self._pending_answers = {}
            self._pending_notes = {}
            self._current_question_idx = 0
            self._log(Text(''))
            self._show_current_question()
        elif self.plan_mode and response and ('PLAN_READY' in response or len(response) > 500):
            self._last_plan_text = response
            self._show_plan_choices()

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []

        # Send queued message if one was waiting
        if self._queued_message and not questions:
            queued = self._queued_message
            self._queued_message = None
            asyncio.create_task(self._send_message(queued))

    async def _on_error(self, msg):
        self.generating = False
        self._update_footer()
        t = self.theme_data
        error = msg.get('error', 'Unknown error')
        self.slog.error_event(error)
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
