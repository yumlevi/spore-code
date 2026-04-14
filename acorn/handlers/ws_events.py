"""WebSocket event handlers — owns streaming state, communicates via bridge."""

import asyncio
from dataclasses import dataclass, field
from rich.text import Text
from rich.panel import Panel
from rich.markdown import Markdown
from rich.rule import Rule

from acorn.questions import parse_questions


@dataclass
class StreamState:
    """State owned by WSEventsHandler — not shared with app."""
    buffer: str = ''
    response_parts: list = field(default_factory=list)
    tool_lines: list = field(default_factory=list)
    started: bool = False


class WSEventsHandler:
    """Handles WebSocket events. Owns streaming state."""

    def __init__(self, bridge):
        self.bridge = bridge
        self.stream = StreamState()
        self.last_response = ''

    def reset_stream(self):
        self.stream = StreamState()

    # ── Event handlers ─────────────────────────────────────────────

    async def on_history(self, msg):
        messages = msg.get('messages', [])
        b = self.bridge

        if not messages:
            writer = b.session_writer
            if writer and writer.message_count > 0:
                t = b.theme
                b.log(Text('  ⚠ Server session expired — local history preserved', style=t.get('warning', 'yellow')))
                b.scroll_bottom()
                from acorn.session_writer import load_session
                local = load_session(b.session_id)
                if local:
                    b.render_local_history(local)
            return

        t = b.theme
        b.log(Rule('Session History', style=t['separator']))
        for m in messages:
            role = m.get('role', 'user')
            text = m.get('text', '')
            if not text.strip():
                continue
            if role == 'user':
                display = text[:300] + '...' if len(text) > 300 else text
                b.log_user_panel(display)
            elif role == 'assistant':
                b.log_bot_panel(text)
        b.log(Rule(style=t['separator']))
        b.scroll_bottom()

    async def on_start(self, msg):
        b = self.bridge
        b.slog.debug('ws', 'chat:start')
        self.reset_stream()

    def flush_stream_buffer(self):
        """Flush accumulated text as a panel — called before tool events and on done."""
        if self.stream.buffer.strip():
            b = self.bridge
            b.log_bot_panel(self.stream.buffer)
            b.scroll_bottom()
        self.stream.buffer = ''
        self.stream.started = False

    async def on_delta(self, msg):
        text = msg.get('text', '')
        self.stream.buffer += text
        self.stream.response_parts.append(text)
        if not self.stream.started:
            self.stream.started = True

    async def on_status(self, msg):
        b = self.bridge
        t = b.theme
        status = msg.get('status', '')
        b.slog.debug('ws:status', status, **{k: v for k, v in msg.items() if k not in ('type', 'status')})

        if status in ('tool_exec_start', 'thinking_start'):
            self.flush_stream_buffer()

        if status == 'thinking_start':
            b.set_activity('thinking...')
            self.stream.tool_lines.append(('thinking', '● Thinking...'))
            self._display_tool_line()
        elif status == 'thinking_done':
            b.set_activity('')
            self.stream.tool_lines = [(k, v) for k, v in self.stream.tool_lines if k != 'thinking']
        elif status == 'tool_exec_start':
            tool = msg.get('tool', '')
            detail = msg.get('detail', '')[:80]
            b.set_activity(f'{tool} {detail[:40]}')
            self.stream.tool_lines.append(('tool_start', f'⚙ {tool} {detail}'))
            self._display_tool_line()
        elif status in ('interjected', 'interjection'):
            b.set_activity('interjecting...')
        elif status == 'waiting':
            b.set_activity('waiting...')
        elif status == 'tool_exec_done':
            b.set_activity('')
            parts = []
            if msg.get('durationMs'):
                parts.append(f'{msg["durationMs"]}ms')
            if msg.get('resultChars'):
                parts.append(f'{msg["resultChars"]:,} chars')
            self.stream.tool_lines.append(('tool_done', f'✓ {" · ".join(parts)}'))
            self._display_tool_line()

    def _display_tool_line(self):
        if not self.stream.tool_lines:
            return
        b = self.bridge
        t = b.theme
        last_type, last_text = self.stream.tool_lines.pop()
        self.stream.tool_lines.clear()
        style_map = {
            'thinking': t['thinking'], 'tool_start': t['tool_icon'],
            'tool_done': t['tool_done'], 'read': t['read_icon'], 'edit': t['edit_icon'],
        }
        b.log(Text(f'  {last_text}', style=style_map.get(last_type, 'dim')))
        b.scroll_bottom()

    async def on_code_view(self, msg):
        t = self.bridge.theme
        path = msg.get('path', '')
        lines = msg.get('content', '').count('\n') + 1
        label = 'new' if msg.get('isNew') else 'read'
        self.stream.tool_lines.append(('read', f'📄 {label} {path} ({lines} lines)'))
        self._display_tool_line()

    async def on_code_diff(self, msg):
        path = msg.get('path', '')
        self.stream.tool_lines.append(('edit', f'✏️  edit {path}'))
        self._display_tool_line()

    async def on_done(self, msg):
        b = self.bridge
        b.generating = False
        b.update_footer()
        b.update_header()

        response = ''.join(self.stream.response_parts)
        usage = msg.get('usage', {})
        server_text = msg.get('text', '')
        b.slog.info('ws:done', f'{len(response)} chars accumulated, {len(server_text)} chars server',
                     iters=msg.get('iterations'), tools=msg.get('toolUsage'),
                     input_tokens=usage.get('input_tokens'), output_tokens=usage.get('output_tokens'))

        if response.strip():
            self.last_response = response
            if hasattr(b, 'session_writer') and b.session_writer:
                b.session_writer.write_assistant(response, usage=usage, iterations=msg.get('iterations'))

        t = b.theme
        self.flush_stream_buffer()

        # Usage stats
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
            b.log(Text(f'  {"  ".join(parts)}', style=t['usage']))

        b.scroll_bottom()

        # Detect questions
        questions = parse_questions(response) if response else []
        b.slog.info('question-detect', f'response={len(response)} chars, questions={len(questions)}',
                     has_marker='QUESTIONS' in response.upper() if response else False)

        if questions and len(questions) >= 1:
            b.log(Text(f'  Agent has {len(questions)} question(s) for you', style=t['accent2']))
            b.scroll_bottom()
            b.get_questions_handler().start_questions(questions)
        elif b.plan_mode and response and 'PLAN_READY' in response:
            plan_handler = b.get_plan_handler()
            plan_handler.state.last_plan_text = response
            plan_handler.show_choices()

        self.reset_stream()

    async def on_error(self, msg):
        b = self.bridge
        b.generating = False
        b.update_footer()
        error = msg.get('error', 'Unknown error')
        b.slog.error_event(error)
        if b.session_writer:
            b.session_writer.write_error(error)
        b.log_error(error)
        b.scroll_bottom()

    async def on_tool(self, msg):
        pass

    async def on_user_message(self, msg):
        """Handle user message from another client (e.g. companion app)."""
        b = self.bridge
        text = msg.get('text', '')
        user = msg.get('userName', 'remote')
        if text:
            b.log_user_panel(text, username=f'{user} (mobile)')
            b.scroll_bottom()

    async def on_remote_approve(self, msg):
        """Handle remote tool approval from companion app."""
        b = self.bridge
        t = b.theme
        tool_id = msg.get('id', '')
        allowed = msg.get('allowed', False)

        # Resolve the pending PromptProvider prompt if one is active.
        # PromptProvider uses _prompt_event / _prompt_result on the app.
        event = b.get_permission_attr('_prompt_event')
        if event and not event.is_set():
            # Build a result that matches what PromptProvider expects
            if allowed:
                # Index 0 = "Allow" (works for both dangerous and non-dangerous)
                b.set_permission_attr('_prompt_result', {'index': 0, 'value': 'allow'})
            else:
                # Last option is always "Deny"
                opts = b.get_permission_attr('_prompt_options', [])
                deny_idx = len(opts) - 1 if opts else 2
                b.set_permission_attr('_prompt_result', {'index': deny_idx, 'value': 'deny'})

            label = '✓ Allowed (mobile)' if allowed else '✗ Denied (mobile)'
            style = t['success'] if allowed else t.get('warning', 'yellow')
            b.log(b.themed_text(f'  {label}', style=style))
            b.scroll_bottom()
            event.set()
        else:
            b.slog.debug('ws', f'remote-approve for {tool_id} but no pending prompt')

    async def on_perm_mode(self, msg):
        """Handle remote permission mode change from companion app."""
        b = self.bridge
        t = b.theme
        mode = msg.get('mode', '')
        if mode in ('ask', 'auto', 'locked', 'yolo'):
            b.permissions.mode = mode
            descs = {'auto': 'auto-approve', 'ask': 'ask for each', 'locked': 'deny all', 'yolo': 'approve everything'}
            b.log(b.themed_text(f'  Mode → {mode}: {descs[mode]} (set from mobile)', style=t['accent']))
            b.scroll_bottom()
            b.update_footer()
