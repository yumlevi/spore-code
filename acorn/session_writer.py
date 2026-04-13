"""Session persistence — write chat history to local JSONL for crash recovery."""

import json
import time
from pathlib import Path
from acorn.config import GLOBAL_DIR

SESSIONS_DIR = GLOBAL_DIR / 'sessions'


class SessionWriter:
    """Writes chat history to ~/.acorn/sessions/<id>.jsonl as it happens."""

    def __init__(self, session_id: str):
        SESSIONS_DIR.mkdir(parents=True, exist_ok=True)
        safe_id = session_id.replace(':', '_').replace('@', '_').replace('/', '_')[:80]
        self.path = SESSIONS_DIR / f'{safe_id}.jsonl'
        self.session_id = session_id
        is_new = not self.path.exists()
        self._file = open(self.path, 'a', buffering=1)
        self.message_count = 0
        # Write metadata header on new files
        if is_new:
            self._append({'_meta': True, 'session_id': session_id, 'created': time.time()})

    def _append(self, record: dict):
        record['ts'] = time.time()
        try:
            self._file.write(json.dumps(record) + '\n')
            self.message_count += 1
        except Exception:
            pass

    def write_user(self, text: str):
        self._append({'role': 'user', 'text': text})

    def write_assistant(self, text: str, usage: dict = None, iterations: int = None):
        record = {'role': 'assistant', 'text': text}
        if usage:
            record['usage'] = usage
        if iterations:
            record['iterations'] = iterations
        self._append(record)

    def write_tool(self, name: str, input_data: dict, result, local: bool, duration_ms: int = 0):
        self._append({
            'role': 'tool',
            'name': name,
            'input': json.dumps(input_data)[:500] if input_data else '',
            'result_preview': json.dumps(result)[:500] if result else '',
            'local': local,
            'ms': duration_ms,
        })

    def write_error(self, error: str):
        self._append({'role': 'error', 'text': error})

    def close(self):
        try:
            self._file.close()
        except Exception:
            pass


def load_session(session_id: str) -> list:
    """Load a session's message history from local JSONL. Skips _meta lines."""
    safe_id = session_id.replace(':', '_').replace('@', '_').replace('/', '_')[:80]
    path = SESSIONS_DIR / f'{safe_id}.jsonl'
    if not path.exists():
        return []
    messages = []
    try:
        for line in open(path):
            try:
                record = json.loads(line.strip())
                if record.get('_meta'):
                    continue
                messages.append(record)
            except json.JSONDecodeError:
                continue
    except Exception:
        pass
    return messages


def list_project_sessions(user: str, cwd: str) -> list:
    """List all saved sessions for a user+project, newest first.

    Returns list of dicts: {session_id, path, modified, message_count, preview}
    """
    from acorn.session import find_git_root
    import os
    import hashlib

    if not SESSIONS_DIR.exists():
        return []

    project_root = find_git_root(cwd) or cwd
    name = os.path.basename(project_root)
    path_hash = hashlib.sha256(project_root.encode()).hexdigest()[:8]
    # Match the safe_id format used by SessionWriter (: → _, @ → _)
    prefix = f'cli_{user}_{name}-{path_hash}'

    sessions = []
    for f in sorted(SESSIONS_DIR.iterdir(), key=lambda p: p.stat().st_mtime, reverse=True):
        if f.suffix == '.jsonl' and f.name.startswith(prefix):
            # Quick scan: count messages and get first user message as preview
            msg_count = 0
            first_user_msg = ''
            last_assistant_msg = ''
            try:
                for line in open(f):
                    try:
                        record = json.loads(line.strip())
                        msg_count += 1
                        if record.get('role') == 'user' and not first_user_msg:
                            first_user_msg = record.get('text', '')[:100]
                        if record.get('role') == 'assistant':
                            last_assistant_msg = record.get('text', '')[:100]
                    except json.JSONDecodeError:
                        continue
            except Exception:
                continue

            if msg_count == 0:
                continue

            # Read session_id from metadata header if available, else reconstruct from filename
            session_id = None
            try:
                first_line = json.loads(open(f).readline())
                if first_line.get('_meta'):
                    session_id = first_line.get('session_id')
            except Exception:
                pass
            if not session_id:
                session_id = f.stem  # fallback — not perfect but usable

            # Format modification time
            import datetime
            mtime = datetime.datetime.fromtimestamp(f.stat().st_mtime)
            age = time.time() - f.stat().st_mtime
            if age < 3600:
                time_ago = f'{int(age / 60)}m ago'
            elif age < 86400:
                time_ago = f'{int(age / 3600)}h ago'
            else:
                time_ago = mtime.strftime('%Y-%m-%d %H:%M')

            sessions.append({
                'session_id': session_id,
                'path': f,
                'modified': mtime,
                'time_ago': time_ago,
                'message_count': msg_count,
                'preview': first_user_msg or last_assistant_msg or '(empty)',
            })

    return sessions


def cleanup_old_sessions(keep_days: int = 30):
    """Remove sessions older than keep_days."""
    if not SESSIONS_DIR.exists():
        return 0
    cutoff = time.time() - (keep_days * 86400)
    removed = 0
    for f in SESSIONS_DIR.iterdir():
        if f.suffix == '.jsonl' and f.stat().st_mtime < cutoff:
            f.unlink()
            removed += 1
    return removed
