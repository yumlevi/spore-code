"""PromptProvider — generic UI prompt API for choices and text input.

Used by questions, permissions, and plan approval instead of
monkeypatching app internals.
"""

import asyncio
from rich.text import Text


class PromptProvider:
    """Shows interactive prompts in the TUI via the question selector widget.

    Usage:
        result = await app.prompter.choice('Pick one', ['A', 'B', 'C'])
        # result = {'index': 1, 'value': 'B'}

        result = await app.prompter.choice('Pick many', ['X', 'Y', 'Z'], multi=True)
        # result = {'indices': [0, 2], 'values': ['X', 'Z']}
    """

    def __init__(self, app):
        self.app = app
        self._event = None
        self._result = None

    async def choice(self, prompt: str, options: list, multi: bool = False) -> dict:
        """Show options in the selector widget, wait for user choice.

        Returns:
            Single-select: {'index': int, 'value': str}
            Multi-select:  {'indices': list[int], 'values': list[str]}
            Cancelled:     {'cancelled': True}
        """
        app = self.app
        t = app.theme_data

        # Show prompt in transcript
        app._log(Text(f'  {prompt}', style=f'bold {t["accent"]}'))
        app._scroll_bottom()

        # Set up the selector
        app._q_selected = 0
        app._q_checked = set()
        app._q_noting = False
        app._q_open_ended = False
        app._q_transitioning = False

        # Store prompt state for the selector
        app._prompt_options = options
        app._prompt_multi = multi
        app._prompt_event = asyncio.Event()
        app._prompt_result = None
        app._prompt_active = True

        # Render and show
        self._render_selector(options, multi)
        app._hide_widget('#user-input')
        app._show_widget('#question-selector')
        try:
            from acorn.ui.widgets import FocusableStatic
            app.query_one('#question-selector', FocusableStatic).focus()
        except Exception:
            pass

        # Wait for user interaction
        await app._prompt_event.wait()

        # Cleanup
        app._prompt_active = False
        app._hide_widget('#question-selector')
        app._show_widget('#user-input')
        try:
            from acorn.ui.widgets import MessageInput
            app.query_one('#user-input', MessageInput).focus()
        except Exception:
            pass

        return app._prompt_result or {'cancelled': True}

    def _render_selector(self, options, multi):
        """Render options in the question-selector widget."""
        app = self.app
        t = app.theme_data
        lines = Text()
        for i, opt in enumerate(options):
            cursor = '▸' if i == app._q_selected else ' '
            if multi:
                check = '◉' if i in app._q_checked else '○'
                if i == app._q_selected:
                    lines.append(f' {cursor} {check} {opt}', style=f'bold {t["accent"]}')
                elif i in app._q_checked:
                    lines.append(f' {cursor} {check} {opt}', style=t['success'])
                else:
                    lines.append(f' {cursor} {check} {opt}', style=t['fg'])
            else:
                if i == app._q_selected:
                    lines.append(f' {cursor}  {opt}', style=f'bold {t["accent"]}')
                else:
                    lines.append(f' {cursor}  {opt}', style=t['fg'])
            lines.append('\n')

        hint = ' ↑↓ move · Space toggle · Enter submit' if multi else ' ↑↓ select · Enter confirm'
        lines.append(hint, style=t.get('muted', 'dim'))

        try:
            from acorn.ui.widgets import FocusableStatic
            app.query_one('#question-selector', FocusableStatic).update(lines)
        except Exception:
            pass

    def handle_key(self, key: str):
        """Handle key events from FocusableStatic during a prompt."""
        app = self.app
        if not getattr(app, '_prompt_active', False):
            return False

        options = app._prompt_options
        multi = app._prompt_multi

        if key == 'up':
            app._q_selected = (app._q_selected - 1) % len(options)
            self._render_selector(options, multi)
            return True
        elif key == 'down':
            app._q_selected = (app._q_selected + 1) % len(options)
            self._render_selector(options, multi)
            return True
        elif key == 'space' and multi:
            if app._q_selected in app._q_checked:
                app._q_checked.discard(app._q_selected)
            else:
                app._q_checked.add(app._q_selected)
            self._render_selector(options, multi)
            return True
        elif key == 'enter':
            if multi:
                indices = sorted(app._q_checked)
                values = [options[i] for i in indices]
                app._prompt_result = {'indices': indices, 'values': values}
            else:
                app._prompt_result = {'index': app._q_selected, 'value': options[app._q_selected]}
            app._prompt_event.set()
            return True
        elif key == 'escape':
            app._prompt_result = {'cancelled': True}
            app._prompt_event.set()
            return True

        return False
