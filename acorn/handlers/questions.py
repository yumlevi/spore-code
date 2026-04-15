"""Questions handler — owns question state, communicates via bridge."""

import asyncio
from dataclasses import dataclass, field
from rich.text import Text

from acorn.questions import format_answers
from acorn.protocol import chat_message


@dataclass
class QuestionState:
    """State owned by QuestionsHandler."""
    questions: list = field(default_factory=list)
    answers: dict = field(default_factory=dict)
    notes: dict = field(default_factory=dict)
    current_idx: int = 0
    selected: int = 0
    checked: set = field(default_factory=set)
    noting: bool = False
    open_ended: bool = False
    transitioning: bool = False
    test_mode: bool = False
    plan_approval: bool = False
    permission_mode: bool = False
    active: bool = False


class QuestionsHandler:
    """Handles interactive question selector flow. Owns its own state."""

    def __init__(self, bridge):
        self.bridge = bridge
        self.state = QuestionState()

    def start_questions(self, questions, test_mode=False):
        """Begin a new question flow."""
        self.state = QuestionState(
            questions=questions,
            active=True,
            test_mode=test_mode,
        )
        # Also set legacy flag for FocusableStatic compatibility
        self.bridge._app._answering_questions = True
        self.bridge.sm.transition(self.bridge.AppState.QUESTIONS)
        self.bridge.log(Text(''))
        self._show_current()

    def _show_current(self):
        s = self.state
        b = self.bridge
        if s.current_idx >= len(s.questions):
            self._exit()
            self._send_answers()
            return

        q = s.questions[s.current_idx]
        t = b.theme
        total = len(s.questions)

        header = Text()
        header.append(f'  Question {s.current_idx + 1}/{total}: ', style=f'bold {t["accent"]}')
        header.append(q['text'], style='bold')
        b.log(header)
        b.scroll_bottom()

        s.selected = 0
        s.checked = set()
        s.noting = False
        s.open_ended = False
        s.transitioning = False

        if q['options']:
            s.active = True
            b.hide_widget('#user-input')
            b.show_widget('#question-selector')
            self._render_selector()
            b.focus_selector()
        else:
            s.active = True
            s.open_ended = True
            b.hide_widget('#question-selector')
            b.show_widget('#user-input')
            b.focus_input()

    def _render_selector(self):
        s = self.state
        q = s.questions[s.current_idx]
        t = self.bridge.theme
        is_multi = q.get('multi', False)

        lines = Text()
        for i, opt in enumerate(q['options']):
            cursor = '▸' if i == s.selected else ' '
            if is_multi:
                check = '◉' if i in s.checked else '○'
                if i == s.selected:
                    lines.append(f' {cursor} {check} {opt}', style=f'bold {t["accent"]}')
                elif i in s.checked:
                    lines.append(f' {cursor} {check} {opt}', style=t['success'])
                else:
                    lines.append(f' {cursor} {check} {opt}', style=t['fg'])
            else:
                if i == s.selected:
                    lines.append(f' {cursor}  {opt}', style=f'bold {t["accent"]}')
                else:
                    lines.append(f' {cursor}  {opt}', style=t['fg'])
            lines.append('\n')

        hint = ' ↑↓ move · Space toggle · Tab notes · Enter submit' if is_multi else ' ↑↓ select · Tab notes · Enter confirm'
        lines.append(hint, style=t.get('muted', 'dim'))
        self.bridge.update_selector(lines)

    def _exit(self):
        s = self.state
        b = self.bridge
        s.active = False
        s.open_ended = False
        s.noting = False
        b.set_permission_attr('_answering_questions', False)
        b.sm.transition(b.AppState.IDLE)
        b.hide_widget('#question-selector')
        b.hide_widget('#note-input')
        b.show_widget('#user-input')
        b.focus_input()

    def handle_key(self, key):
        """Handle key from FocusableStatic. Returns True if consumed."""
        s = self.state
        if not s.active or s.open_ended or s.noting:
            return False

        q = s.questions[s.current_idx] if s.current_idx < len(s.questions) else None
        if not q or not q.get('options'):
            return False

        is_multi = q.get('multi', False)
        b = self.bridge

        if key == 'up':
            s.selected = (s.selected - 1) % len(q['options'])
            self._render_selector()
        elif key == 'down':
            s.selected = (s.selected + 1) % len(q['options'])
            self._render_selector()
        elif key == 'space' and is_multi:
            if s.selected in s.checked:
                s.checked.discard(s.selected)
            else:
                s.checked.add(s.selected)
            self._render_selector()
        elif key == 'tab':
            s.noting = True
            b.hide_widget('#question-selector')
            b.show_widget('#note-input')
            try:
                from textual.widgets import Input
                note_inp = b.query_note_input()
                note_inp.value = s.notes.get(s.current_idx, '')
                note_inp.focus()
            except Exception:
                pass
        elif key == 'enter':
            self._submit()
        elif key == 'escape':
            self._exit()
            b.log(Text('  Questions cancelled', style=b.theme['muted']))
            b.scroll_bottom()
        else:
            return False
        return True

    def _submit(self):
        s = self.state
        b = self.bridge
        q = s.questions[s.current_idx]
        t = b.theme

        if q['options'] and q.get('multi'):
            selected = [q['options'][i] for i in sorted(s.checked)]
            s.answers[s.current_idx] = selected if selected else ['(none)']
        elif q['options']:
            s.answers[s.current_idx] = q['options'][s.selected]

        # Plan approval routing
        if s.plan_approval:
            s.plan_approval = False
            self._exit()
            choice = s.selected
            ph = b.get_plan_handler()
            if choice == 0:
                b.log(Text(f'  → Execute', style=t['success']))
                ph.handle_decision('1')
            elif choice == 1:
                b.log(Text(f'  → Revise', style=t['accent']))
                ph.handle_decision('2')
            else:
                b.log(Text(f'  → Cancel', style=t['muted']))
                ph.handle_decision('3')
            b.scroll_bottom()
            return

        # Permission routing
        if s.permission_mode:
            s.permission_mode = False
            self._exit()
            # Result handled by permissions.py via _prompt_result/_prompt_event
            dangerous = b.get_permission_attr('_permission_dangerous', False)
            rule = b.get_permission_attr('_permission_rule', '')
            if dangerous:
                allowed = (s.selected == 0)
            else:
                allowed = (s.selected in (0, 1))
                if s.selected == 1 and rule:
                    b.permissions.session_rules.add(rule)
                    b.log(Text(f'  ✓ Rule added: {rule}', style=t['success']))
            if allowed:
                b.log(Text(f'  ✓ Allowed', style=t['success']))
            else:
                b.log(Text(f'  ✗ Denied', style=t.get('warning', 'yellow')))
            b.scroll_bottom()
            b.set_permission_attr('_permission_result', allowed)
            event = b.get_permission_attr('_permission_event')
            if event:
                event.set()
            return

        # Normal question answer
        answer = s.answers.get(s.current_idx, '')
        display = ', '.join(answer) if isinstance(answer, list) else str(answer)
        note = s.notes.get(s.current_idx)
        log_text = Text()
        log_text.append(f'  → {display}', style=t['success'])
        if note:
            log_text.append(f'  ({note})', style=t['muted'])
        b.log(log_text)
        b.scroll_bottom()

        s.current_idx += 1
        b.hide_widget('#question-selector')
        s.active = False
        b.set_permission_attr('_answering_questions', False)

        def _next():
            s.active = True
            b.set_permission_attr('_answering_questions', True)
            b.sm.transition(b.AppState.QUESTIONS)
            self._show_current()
        b.set_timer(0.15, _next)

    def handle_text_answer(self, text):
        """Handle text input for open-ended questions or note input."""
        s = self.state
        b = self.bridge
        t = b.theme

        if s.noting:
            s.noting = False
            if text.strip():
                s.notes[s.current_idx] = text.strip()
            b.hide_widget('#note-input')
            b.show_widget('#question-selector')
            self._render_selector()
            b.focus_selector()
            return

        # Open-ended answer
        s.answers[s.current_idx] = text
        b.log(Text(f'  → {text}', style=t['success']))
        b.scroll_bottom()
        s.current_idx += 1
        s.active = False
        b.set_permission_attr('_answering_questions', False)

        def _next():
            s.active = True
            b.set_permission_attr('_answering_questions', True)
            b.sm.transition(b.AppState.QUESTIONS)
            self._show_current()
        b.set_timer(0.15, _next)

    def _send_answers(self):
        s = self.state
        b = self.bridge
        answers_data = {'answers': s.answers, 'notes': s.notes}
        formatted = format_answers(s.questions, answers_data)
        t = b.theme

        b.log(Text(''))
        b.log_user_panel(formatted)
        b.scroll_bottom()

        if s.test_mode:
            b.log(Text('  ✓ Questions completed (test mode)', style=t['success']))
            b.scroll_bottom()
            return

        b.generating = True
        b.update_footer()
        b.update_header()
        asyncio.create_task(
            b.conn.send(chat_message(b.session_id, formatted, b.user, cwd=b.cwd))
        )
        # Broadcast so mobile dismisses its question sheet
        b.broadcast('interactive:resolved', kind='questions')
