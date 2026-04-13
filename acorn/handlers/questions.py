"""Question handler mixin — interactive question selector flow."""

import asyncio
from rich.text import Text

from acorn.questions import format_answers
from acorn.protocol import chat_message


class QuestionsMixin:
    """Mixin providing interactive question handling for AcornApp."""

    def _show_current_question(self):
        """Show the current question — replace input area with selector for options."""
        questions = getattr(self, '_pending_questions', [])
        idx = getattr(self, '_current_question_idx', 0)
        if idx >= len(questions):
            self._exit_question_mode()
            self._send_question_answers()
            return
        q = questions[idx]
        t = self.theme_data
        total = len(questions)

        # Show question in transcript
        header = Text()
        header.append(f'  Question {idx + 1}/{total}: ', style=f'bold {t["accent"]}')
        header.append(q['text'], style='bold')
        self._log(header)
        self._scroll_bottom()

        self._q_selected = 0
        self._q_checked = set()
        self._q_noting = False
        self._q_open_ended = False
        self._q_transitioning = False

        if q['options']:
            # Show selector in bottom area, hide regular input
            self._answering_questions = True
            self._hide_widget('#user-input')
            self._show_widget('#question-selector')
            self._render_question_selector()
            try:
                from acorn.ui.widgets import FocusableStatic
                self.query_one('#question-selector', FocusableStatic).focus()
            except Exception:
                pass
        else:
            # Open-ended: show regular input, hide selector
            self._answering_questions = True
            self._q_open_ended = True
            self._hide_widget('#question-selector')
            self._show_widget('#user-input')
            try:
                from acorn.ui.widgets import MessageInput
                inp = self.query_one('#user-input', MessageInput)
                inp.clear()
                inp.focus()
            except Exception:
                pass

    def _render_question_selector(self):
        """Render the option selector in the bottom area."""
        questions = self._pending_questions
        idx = self._current_question_idx
        q = questions[idx]
        t = self.theme_data
        is_multi = q.get('multi', False)

        lines = Text()
        for i, opt in enumerate(q['options']):
            cursor = '▸' if i == self._q_selected else ' '
            if is_multi:
                check = '◉' if i in self._q_checked else '○'
                if i == self._q_selected:
                    lines.append(f' {cursor} {check} {opt}', style=f'bold {t["accent"]}')
                elif i in self._q_checked:
                    lines.append(f' {cursor} {check} {opt}', style=t['success'])
                else:
                    lines.append(f' {cursor} {check} {opt}', style=t['fg'])
            else:
                if i == self._q_selected:
                    lines.append(f' {cursor}  {opt}', style=f'bold {t["accent"]}')
                else:
                    lines.append(f' {cursor}  {opt}', style=t['fg'])
            lines.append('\n')

        # Hint line
        if is_multi:
            lines.append(' ↑↓ move · Space toggle · Tab notes · Enter submit', style=t['muted'])
        else:
            lines.append(' ↑↓ select · Tab notes · Enter confirm', style=t['muted'])

        try:
            from acorn.ui.widgets import FocusableStatic
            self.query_one('#question-selector', FocusableStatic).update(lines)
        except Exception:
            pass

    def _show_widget(self, selector):
        try:
            self.query_one(selector).remove_class('hidden')
        except Exception:
            pass

    def _hide_widget(self, selector):
        try:
            self.query_one(selector).add_class('hidden')
        except Exception:
            pass

    def _exit_question_mode(self):
        """Restore the normal input area."""
        self._answering_questions = False
        self._q_open_ended = False
        self._q_noting = False
        self._hide_widget('#question-selector')
        self._hide_widget('#note-input')
        self._show_widget('#user-input')
        try:
            from acorn.ui.widgets import MessageInput
            inp = self.query_one('#user-input', MessageInput)
            inp.clear()
            inp.focus()
        except Exception:
            pass

    def _handle_question_key(self, key):
        """Handle key events during question selector mode."""
        questions = self._pending_questions
        idx = self._current_question_idx
        if idx >= len(questions):
            return
        q = questions[idx]
        is_multi = q.get('multi', False)

        if key == 'up':
            self._q_selected = (self._q_selected - 1) % len(q['options'])
            self._render_question_selector()
        elif key == 'down':
            self._q_selected = (self._q_selected + 1) % len(q['options'])
            self._render_question_selector()
        elif key == 'space' and is_multi:
            if self._q_selected in self._q_checked:
                self._q_checked.discard(self._q_selected)
            else:
                self._q_checked.add(self._q_selected)
            self._render_question_selector()
        elif key == 'tab':
            # Show note input
            self._q_noting = True
            self._hide_widget('#question-selector')
            self._show_widget('#note-input')
            try:
                from textual.widgets import Input
                note_inp = self.query_one('#note-input', Input)
                note_inp.value = self._pending_notes.get(idx, '')
                note_inp.focus()
            except Exception:
                pass
        elif key == 'enter':
            self._submit_question_answer()
        elif key == 'escape':
            self._exit_question_mode()
            t = self.theme_data
            self._log(Text('  Questions cancelled', style=t['muted']))
            self._scroll_bottom()

    def _submit_question_answer(self):
        """Submit the current question's answer and move to next."""
        questions = self._pending_questions
        idx = self._current_question_idx
        q = questions[idx]
        t = self.theme_data

        if q['options'] and q.get('multi'):
            selected = [q['options'][i] for i in sorted(self._q_checked)]
            self._pending_answers[idx] = selected if selected else ['(none)']
        elif q['options']:
            self._pending_answers[idx] = q['options'][self._q_selected]

        answer = self._pending_answers.get(idx, '')
        display = ', '.join(answer) if isinstance(answer, list) else str(answer)

        # Plan approval mode — route to plan handler
        if getattr(self, '_q_plan_approval', False):
            self._q_plan_approval = False
            self._answering_questions = False
            self._exit_question_mode()
            choice = self._q_selected
            if choice == 0:
                self._log(Text(f'  → Execute', style=t['success']))
                self._handle_plan_decision('1')
            elif choice == 1:
                self._log(Text(f'  → Revise', style=t['accent']))
                self._handle_plan_decision('2')
            else:
                self._log(Text(f'  → Cancel', style=t['muted']))
                self._handle_plan_decision('3')
            self._scroll_bottom()
            return

        # Permission approval mode
        if getattr(self, '_q_permission_mode', False):
            self._q_permission_mode = False
            self._answering_questions = False
            self._exit_question_mode()
            choice = self._q_selected
            dangerous = getattr(self, '_permission_dangerous', False)
            rule = getattr(self, '_permission_rule', '')

            if dangerous:
                allowed = (choice == 0)
            else:
                allowed = (choice in (0, 1))
                if choice == 1 and rule:
                    self.permissions.session_rules.add(rule)
                    self._log(Text(f'  ✓ Rule added for session: {rule}', style=t['success']))

            if allowed:
                self._log(Text(f'  ✓ Allowed', style=t['success']))
            else:
                self._log(Text(f'  ✗ Denied', style=t['warning']))
            self._scroll_bottom()

            self._permission_result = allowed
            event = getattr(self, '_permission_event', None)
            if event:
                event.set()
            return

        note = self._pending_notes.get(idx)
        log_text = Text()
        log_text.append(f'  → {display}', style=t['success'])
        if note:
            log_text.append(f'  ({note})', style=t['muted'])
        self._log(log_text)
        self._scroll_bottom()

        self._current_question_idx += 1

        # Hide selector during transition, show next after brief pause
        self._hide_widget('#question-selector')
        self._answering_questions = False

        def _next():
            self._answering_questions = True
            self._show_current_question()
        self.set_timer(0.15, _next)

    def _handle_question_answer(self, text):
        """Handle text input for open-ended questions or note input."""
        if self._q_noting:
            # Save note and return to selector
            self._q_noting = False
            idx = self._current_question_idx
            if text.strip():
                self._pending_notes[idx] = text.strip()
            self._hide_widget('#note-input')
            self._show_widget('#question-selector')
            self._render_question_selector()
            try:
                from acorn.ui.widgets import FocusableStatic
                self.query_one('#question-selector', FocusableStatic).focus()
            except Exception:
                pass
            return

        # Open-ended answer
        idx = self._current_question_idx
        self._pending_answers[idx] = text
        t = self.theme_data
        self._log(Text(f'  → {text}', style=t['success']))
        self._scroll_bottom()
        self._current_question_idx += 1
        self._q_transitioning = True
        def _next():
            self._q_transitioning = False
            self._show_current_question()
        self.set_timer(0.15, _next)

    def _send_question_answers(self):
        """Format and send all answers back to the agent."""
        self._answering_questions = False
        questions = self._pending_questions
        answers_data = {'answers': self._pending_answers, 'notes': self._pending_notes}
        formatted = format_answers(questions, answers_data)
        t = self.theme_data

        self._log(Text(''))
        self._log(self._themed_panel(formatted, title=f'[bold]{self.user}[/bold]', border_style=t['prompt_user']))
        self._scroll_bottom()

        # In test mode, just display — don't send to agent
        if getattr(self, '_q_test_mode', False):
            self._q_test_mode = False
            self._log(Text('  ✓ Questions completed (test mode — not sent)', style=t['success']))
            self._scroll_bottom()
            return

        self._stream_buffer = ''
        self._response_text = []
        self._tool_lines = []
        self.generating = True
        self._update_footer()
        self._update_header()
        asyncio.create_task(
            self.conn.send(chat_message(self.session_id, formatted, self.user))
        )
