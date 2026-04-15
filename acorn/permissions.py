"""Permission system — three modes with session-scoped allow rules."""

import re

# Tools that never need approval
ALWAYS_SAFE = {'read_file', 'glob', 'grep'}

# Patterns that ALWAYS require approval even in auto mode
DANGEROUS_PATTERNS = [
    # Unix
    r'\brm\s+(-rf?|--recursive)', r'\brm\s+/', r'rmdir\s+/',
    r'\bmkfs\b', r'>\s*/dev/', r'dd\s+if=',
    r'chmod\s+(-R\s+)?777', r'chown\s+-R\s+.*/',
    r'\bgit\s+push\s+.*--force', r'\bgit\s+reset\s+--hard',
    r'\bdrop\s+table\b', r'\bdrop\s+database\b',
    r'\btruncate\s+table\b',
    r'\bmkfs\.\w+\b', r'\bfdisk\b', r'\bparted\b',
    r':()\{', r'curl.*\|\s*(ba)?sh', r'wget.*\|\s*(ba)?sh',
    r'\bkill\s+-9\b',
    # Windows
    r'\bdel\s+/[sq]', r'\brd\s+/s', r'\brmdir\s+/s',
    r'\bformat\s+[a-zA-Z]:', r'\bdiskpart\b',
    r'Remove-Item.*-Recurse.*-Force', r'Stop-Process.*-Force',
    r'Clear-Content.*-Force', r'Set-ExecutionPolicy\s+Unrestricted',
]

DANGEROUS_RE = [re.compile(p, re.IGNORECASE) for p in DANGEROUS_PATTERNS]


def is_dangerous(tool_name: str, input: dict) -> bool:
    """Check if a tool call matches dangerous patterns."""
    if tool_name == 'exec':
        cmd = input.get('command', '')
        return any(r.search(cmd) for r in DANGEROUS_RE)
    if tool_name == 'write_file':
        path = input.get('path', '')
        # Writing to system paths
        if path.startswith('/etc/') or path.startswith('/usr/') or path.startswith('/bin/'):
            return True
    return False


def summarize(tool_name: str, input: dict) -> str:
    """Human-readable summary of a tool call."""
    if tool_name == 'exec':
        return input.get('command', '')[:120]
    if tool_name in ('write_file', 'edit_file', 'read_file'):
        return input.get('path', '')
    if tool_name == 'web_fetch':
        return input.get('url', '')[:100]
    if tool_name == 'web_serve':
        return input.get('dir', input.get('directory', ''))[:80]
    return str(input)[:80]


def make_rule(tool_name: str, input: dict) -> str:
    """Generate a session allow-rule from a tool call.
    e.g. exec:git* for 'git status', write_file:src/* for 'src/app.py'
    """
    if tool_name == 'exec':
        cmd = input.get('command', '').strip()
        first_word = cmd.split()[0] if cmd else ''
        if '/' in first_word:
            first_word = first_word.rsplit('/', 1)[-1]
        return f'exec:{first_word}*' if first_word else 'exec:*'
    if tool_name in ('write_file', 'edit_file'):
        path = input.get('path', '')
        if '/' in path:
            dir_part = path.rsplit('/', 1)[0]
            return f'{tool_name}:{dir_part}/*'
        return f'{tool_name}:*'
    return f'{tool_name}:*'


def matches_rule(rule: str, tool_name: str, input: dict) -> bool:
    """Check if a tool call matches an allow rule."""
    if ':' not in rule:
        return rule == tool_name
    rule_tool, rule_pattern = rule.split(':', 1)
    if rule_tool != tool_name:
        return False
    if rule_pattern == '*':
        return True

    if tool_name == 'exec':
        cmd = input.get('command', '').strip()
        # Pattern like "git*" matches "git status", "git push", etc.
        if rule_pattern.endswith('*'):
            prefix = rule_pattern[:-1]
            return cmd.startswith(prefix) or cmd.split()[0] == prefix.rstrip()
        return cmd.startswith(rule_pattern)
    if tool_name in ('write_file', 'edit_file'):
        path = input.get('path', '')
        if rule_pattern.endswith('/*'):
            dir_prefix = rule_pattern[:-2]
            return path.startswith(dir_prefix + '/')
        return path == rule_pattern
    return False


class TuiPermissions:
    """Permission system for the TUI with three modes:

    - auto: approve everything except dangerous commands
    - ask: prompt for every non-safe tool, with option to add session rules
    - locked: approve nothing that isn't in ALWAYS_SAFE
    """

    MODES = ('ask', 'auto', 'locked', 'yolo')

    def __init__(self, app=None, renderer=None):
        self.app = app
        self.renderer = renderer  # for one-shot mode fallback
        self.mode = 'auto' if app is None else 'ask'
        self.session_rules = set()
        self.approve_all = False

    def is_auto_approved(self, tool_name: str, input: dict) -> bool:
        if tool_name in ALWAYS_SAFE:
            return True
        if self.mode == 'yolo':
            return True  # everything approved, no exceptions
        if self.approve_all or self.mode == 'auto':
            if is_dangerous(tool_name, input):
                return False  # dangerous always asks
            return True
        if self.mode == 'locked':
            return False
        # ask mode — check session rules
        for rule in self.session_rules:
            if matches_rule(rule, tool_name, input):
                return True
        return False

    def _notify_companion(self, tool_name, summary, dangerous):
        """Send tool:awaiting-approval to companion app."""
        try:
            import json, asyncio
            conn = self.app.conn
            if conn and conn.connected:
                asyncio.ensure_future(conn.send(json.dumps({
                    'type': 'tool:awaiting-approval',
                    'name': tool_name,
                    'summary': summary,
                    'dangerous': dangerous,
                })))
        except Exception:
            pass

    async def prompt(self, tool_name: str, input: dict) -> bool:
        """Show approval UI. Uses TUI selector if app is available, console prompt otherwise."""
        summary = summarize(tool_name, input)
        rule = make_rule(tool_name, input)

        # One-shot / non-TUI mode — simple console prompt
        if not self.app:
            from rich.prompt import Confirm
            loop = __import__('asyncio').get_event_loop()
            return await loop.run_in_executor(
                None, lambda: Confirm.ask(f'  Allow [bold]{tool_name}[/bold]: {summary}?', default=True)
            )
        dangerous = is_dangerous(tool_name, input)

        from rich.text import Text
        t = self.app.theme_data

        # Show what's being requested
        label = Text()
        label.append(f'  ⚙ {tool_name}: ', style=f'bold {t["accent"]}')
        label.append(summary, style=t['fg'])
        if dangerous:
            label.append('  ⚠ DANGEROUS', style=t['error'])
        self.app._log(label)
        self.app._scroll_bottom()

        # Build options
        if dangerous:
            options = ['✓ Allow (once)', '✗ Deny']
        else:
            options = ['✓ Allow', f'✓ Allow all {rule}', '✗ Deny']

        # Use PromptProvider — serialized with lock, one prompt at a time.
        # Notify companion app right before showing the prompt (inside the lock
        # ensures only one notification at a time, matching one prompt at a time).
        result = await self.app.prompter.choice(
            f'Allow {tool_name}?', options,
            on_show=lambda: self._notify_companion(tool_name, summary, dangerous),
        )

        if result.get('cancelled'):
            return False

        choice = result.get('index', -1)
        if dangerous:
            allowed = (choice == 0)
        else:
            allowed = (choice in (0, 1))
            if choice == 1 and rule:
                self.session_rules.add(rule)
                self.app._log(Text(f'  ✓ Rule added for session: {rule}', style=t['success']))

        if allowed:
            self.app._log(Text(f'  ✓ Allowed', style=t['success']))
        else:
            self.app._log(Text(f'  ✗ Denied', style=t.get('warning', 'yellow')))
        self.app._scroll_bottom()

        # Broadcast to companion app so approval cards dismiss
        try:
            self.app.bridge.broadcast('interactive:resolved', kind='tool-approval', allowed=allowed)
        except Exception:
            pass

        return allowed
