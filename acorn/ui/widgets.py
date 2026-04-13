"""Custom Textual widgets for the Acorn TUI."""

from textual.widgets import Static, RichLog, TextArea


class MessageInput(TextArea):
    """TextArea that submits on Enter, cycles history with Up/Down,
    stashes with Ctrl+S, and inserts newline on Ctrl+J."""

    class Submitted:
        """Fired when user presses Enter to submit."""
        def __init__(self, text):
            self.text = text

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._history = []        # sent messages, newest last
        self._history_idx = -1    # -1 = current input, 0+ = browsing history
        self._draft = ''          # current unsent text saved when browsing
        self._stash = []          # stashed messages (Ctrl+S to stash, Ctrl+R to pop)

    def on_key(self, event):
        app = self.app

        # If autocomplete is showing, route keys to it
        if getattr(app, '_autocomplete_matches', []):
            if event.key == 'enter' or event.key == 'tab':
                idx = getattr(app, '_autocomplete_selected', 0)
                matches = app._autocomplete_matches
                if idx < len(matches):
                    cmd, _ = matches[idx]
                    self.clear()
                    self.insert(cmd + ' ')
                app._autocomplete_matches = []
                app._hide_widget('#autocomplete')
                event.prevent_default()
                event.stop()
                return
            elif event.key == 'up':
                app._autocomplete_selected = (app._autocomplete_selected - 1) % min(len(app._autocomplete_matches), 8)
                app._render_autocomplete()
                event.prevent_default()
                event.stop()
                return
            elif event.key == 'down':
                app._autocomplete_selected = (app._autocomplete_selected + 1) % min(len(app._autocomplete_matches), 8)
                app._render_autocomplete()
                event.prevent_default()
                event.stop()
                return
            elif event.key == 'escape':
                app._autocomplete_matches = []
                app._hide_widget('#autocomplete')
                event.prevent_default()
                event.stop()
                return

        # Up arrow → previous message in history
        if event.key == 'up':
            if self._history:
                if self._history_idx == -1:
                    # Save current draft before browsing
                    self._draft = self.text
                    self._history_idx = len(self._history) - 1
                elif self._history_idx > 0:
                    self._history_idx -= 1
                self.clear()
                self.insert(self._history[self._history_idx])
            event.prevent_default()
            event.stop()
            return

        # Down arrow → next message in history or back to draft
        if event.key == 'down':
            if self._history_idx >= 0:
                if self._history_idx < len(self._history) - 1:
                    self._history_idx += 1
                    self.clear()
                    self.insert(self._history[self._history_idx])
                else:
                    # Back to draft
                    self._history_idx = -1
                    self.clear()
                    self.insert(self._draft)
            event.prevent_default()
            event.stop()
            return

        # Enter → send message
        if event.key == 'enter':
            text = self.text.strip()
            if text:
                # Add to history
                if not self._history or self._history[-1] != text:
                    self._history.append(text)
                    # Cap at 100
                    if len(self._history) > 100:
                        self._history = self._history[-100:]
                self._history_idx = -1
                self._draft = ''
                if hasattr(app, 'on_message_input_submitted'):
                    app.on_message_input_submitted(self.Submitted(text))
                self.clear()
            event.prevent_default()
            event.stop()
            return

        # Ctrl+J → insert newline
        if event.key == 'ctrl+j':
            self.insert('\n')
            event.prevent_default()
            event.stop()
            return

        # Ctrl+S → stash current message
        if event.key == 'ctrl+s':
            text = self.text.strip()
            if text:
                self._stash.append(text)
                self.clear()
                if hasattr(app, '_log'):
                    from rich.text import Text
                    t = getattr(app, 'theme_data', {})
                    app._log(Text(f'  📌 Stashed ({len(self._stash)} saved)', style=t.get('muted', 'dim')))
                    app._scroll_bottom()
            event.prevent_default()
            event.stop()
            return

        # Ctrl+R → pop stashed message
        if event.key == 'ctrl+r':
            if self._stash:
                stashed = self._stash.pop()
                # Save current text to stash if non-empty
                current = self.text.strip()
                if current:
                    self._stash.append(current)
                self.clear()
                self.insert(stashed)
                if hasattr(app, '_log'):
                    from rich.text import Text
                    t = getattr(app, 'theme_data', {})
                    remaining = len(self._stash)
                    app._log(Text(f'  📌 Restored stash ({remaining} remaining)', style=t.get('muted', 'dim')))
                    app._scroll_bottom()
            else:
                if hasattr(app, '_log'):
                    from rich.text import Text
                    t = getattr(app, 'theme_data', {})
                    app._log(Text('  📌 Stash empty', style=t.get('muted', 'dim')))
                    app._scroll_bottom()
            event.prevent_default()
            event.stop()
            return


class FocusableStatic(Static):
    """A Static widget that can receive focus for key events.
    Routes to PromptProvider first, then question handler as fallback."""
    can_focus = True

    def on_key(self, event):
        app = self.app

        # Route to PromptProvider if a prompt is active
        if getattr(app, '_prompt_active', False) and hasattr(app, 'prompter'):
            if event.key in ('up', 'down', 'space', 'enter', 'escape'):
                if app.prompter.handle_key(event.key):
                    event.prevent_default()
                    event.stop()
                    return

        # Fallback: question handler
        if not getattr(app, '_answering_questions', False):
            return
        if getattr(app, '_q_transitioning', False):
            event.prevent_default()
            event.stop()
            return
        if event.key in ('up', 'down', 'space', 'tab', 'enter', 'escape'):
            app._handle_question_key(event.key)
            event.prevent_default()
            event.stop()


class SelectableLog(RichLog):
    """RichLog that doesn't capture mouse click/drag, allowing terminal-native text selection.
    Mouse scroll is preserved for scrolling through conversation."""

    def _on_mouse_down(self, event):
        pass  # Let terminal handle click/drag for text selection

    def _on_mouse_up(self, event):
        pass

    # Scroll events are inherited from RichLog and still work
