"""Session ID computation from user + project directory."""

import hashlib
import os
import subprocess
import time


def find_git_root(cwd: str) -> "str | None":
    try:
        result = subprocess.run(
            ['git', 'rev-parse', '--show-toplevel'],
            cwd=cwd, capture_output=True, text=True, timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return None


def get_git_branch(cwd: str) -> str:
    try:
        result = subprocess.run(
            ['git', 'branch', '--show-current'],
            cwd=cwd, capture_output=True, text=True, timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return ''


def compute_session_id(user: str, cwd: str) -> str:
    """New unique session each invocation — includes timestamp so each run is fresh."""
    project_root = find_git_root(cwd) or cwd
    name = os.path.basename(project_root)
    path_hash = hashlib.sha256(project_root.encode()).hexdigest()[:8]
    ts = hex(int(time.time()))[2:]
    return f'cli:{user}@{name}-{path_hash}-{ts}'


def project_name(cwd: str) -> str:
    project_root = find_git_root(cwd) or cwd
    return os.path.basename(project_root)
