"""Session logging — verbose diagnostics for every acorn session."""

import os
import time
import json
import traceback
from pathlib import Path
from datetime import datetime

from acorn.config import GLOBAL_DIR

LOGS_DIR = GLOBAL_DIR / 'logs'


class SessionLogger:
    """Verbose logger for a single acorn session. Writes to ~/.acorn/logs/<session>.log"""

    def __init__(self, session_id: str, user: str, cwd: str):
        LOGS_DIR.mkdir(parents=True, exist_ok=True)

        # Unique filename: timestamp + session hash
        ts = datetime.now().strftime('%Y%m%d-%H%M%S')
        safe_id = session_id.replace(':', '_').replace('@', '_').replace('/', '_')[:60]
        self.filename = f'{ts}_{safe_id}.log'
        self.filepath = LOGS_DIR / self.filename
        self._file = open(self.filepath, 'a', buffering=1)  # line-buffered

        # Write header
        self._write_header(session_id, user, cwd)

    def _write_header(self, session_id, user, cwd):
        import platform
        self._raw(f'=== Acorn Session Log ===')
        self._raw(f'Session:  {session_id}')
        self._raw(f'User:     {user}')
        self._raw(f'CWD:      {cwd}')
        self._raw(f'Started:  {datetime.now().isoformat()}')
        self._raw(f'Platform: {platform.system()} {platform.release()} ({platform.machine()})')
        self._raw(f'Python:   {platform.python_version()}')
        self._raw(f'PID:      {os.getpid()}')
        self._raw(f'===')
        self._raw('')

    def _ts(self):
        return datetime.now().strftime('%H:%M:%S.%f')[:-3]

    def _raw(self, line):
        try:
            self._file.write(line + '\n')
        except Exception:
            pass

    def _log(self, level, category, message, **extra):
        ts = self._ts()
        line = f'[{ts}] [{level}] [{category}] {message}'
        if extra:
            details = ' '.join(f'{k}={_truncate(v)}' for k, v in extra.items())
            line += f' | {details}'
        self._raw(line)

    # ── Public API ──────────────────────────────────────────────────

    def info(self, category, message, **extra):
        self._log('INFO', category, message, **extra)

    def warn(self, category, message, **extra):
        self._log('WARN', category, message, **extra)

    def error(self, category, message, **extra):
        self._log('ERROR', category, message, **extra)

    def debug(self, category, message, **extra):
        self._log('DEBUG', category, message, **extra)

    # ── Structured events ───────────────────────────────────────────

    def auth(self, host, port, user, success, error=None):
        self.info('auth', f'{"OK" if success else "FAILED"} {user}@{host}:{port}',
                  error=error or '')

    def ws_connect(self, url, success, error=None):
        self.info('ws', f'{"connected" if success else "FAILED"} {url}',
                  error=error or '')

    def ws_disconnect(self, reason=''):
        self.info('ws', f'disconnected', reason=reason)

    def message_sent(self, session_id, content_length, has_context=False):
        self.info('msg:out', f'sent {content_length} chars',
                  session=session_id, context=has_context)

    def message_received(self, msg_type, **extra):
        self.debug('msg:in', msg_type, **extra)

    def delta_received(self, chars):
        self.debug('stream', f'+{chars} chars')

    def tool_request(self, tool_name, input_data):
        self.info('tool:req', tool_name,
                  input=json.dumps(input_data)[:500] if input_data else '')

    def tool_result(self, tool_name, result, local, duration_ms=0):
        is_error = isinstance(result, dict) and 'error' in result
        level = 'ERROR' if is_error else 'INFO'
        where = 'local' if local else 'server'
        result_preview = json.dumps(result)[:300] if result else ''
        self._log(level, 'tool:res', f'{tool_name} ({where}) {duration_ms}ms',
                  result=result_preview)

    def permission_check(self, tool_name, auto_approved, mode, dangerous=False):
        self.debug('perm', f'{tool_name} auto={auto_approved} mode={mode} dangerous={dangerous}')

    def permission_prompt(self, tool_name, choice, rule_added=None):
        self.info('perm:prompt', f'{tool_name} → {choice}',
                  rule=rule_added or '')

    def question_detected(self, count):
        self.info('questions', f'detected {count} questions')

    def question_answered(self, index, answer):
        self.info('questions', f'Q{index+1} → {_truncate(str(answer))}')

    def plan_ready(self, plan_length):
        self.info('plan', f'plan ready ({plan_length} chars)')

    def plan_decision(self, action, feedback=None):
        self.info('plan', f'decision: {action}', feedback=feedback or '')

    def command(self, cmd, args=''):
        self.info('cmd', f'{cmd} {args}'.strip())

    def mode_change(self, from_mode, to_mode):
        self.info('mode', f'{from_mode} → {to_mode}')

    def theme_change(self, theme_name):
        self.info('theme', theme_name)

    def error_event(self, error_text, tb=None):
        self.error('error', error_text)
        if tb:
            self._raw(f'  Traceback:\n{tb}')

    def bg_launch(self, pid, command):
        self.info('bg', f'launched #{pid}: {command[:100]}')

    def bg_done(self, pid, exit_code, output_lines):
        self.info('bg', f'#{pid} done exit={exit_code} lines={output_lines}')

    def spinner_start(self):
        self.debug('ui', 'spinner started')

    def spinner_stop(self):
        self.debug('ui', 'spinner stopped')

    def session_end(self, messages_sent, duration_secs):
        self.info('session', f'ended after {messages_sent} messages, {duration_secs:.0f}s')
        self._raw(f'\n=== Session ended {datetime.now().isoformat()} ===')

    def close(self):
        try:
            self._file.close()
        except Exception:
            pass

    def exception(self, category, error):
        """Log an exception with full traceback."""
        tb = traceback.format_exc()
        self.error(category, str(error))
        self._raw(tb)


def _truncate(val, max_len=200):
    s = str(val)
    return s[:max_len] + '...' if len(s) > max_len else s


def cleanup_old_logs(keep_days=14):
    """Remove logs older than keep_days."""
    if not LOGS_DIR.exists():
        return 0
    cutoff = time.time() - (keep_days * 86400)
    removed = 0
    for f in LOGS_DIR.iterdir():
        if f.suffix == '.log' and f.stat().st_mtime < cutoff:
            f.unlink()
            removed += 1
    return removed
