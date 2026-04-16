"""Auto-updater — checks for new versions via git or GitHub API, handles install."""

import os
import subprocess
import json
import re

GITHUB_REPO = 'yumlevi/acorn-cli'
GITHUB_API = f'https://api.github.com/repos/{GITHUB_REPO}'


def get_repo_dir():
    """Get the acorn-cli repo directory."""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def is_git_repo():
    return os.path.isdir(os.path.join(get_repo_dir(), '.git'))


_last_error = ''


def _git(args, cwd=None):
    """Run a git command and return stdout, or None on failure."""
    global _last_error
    try:
        r = subprocess.run(
            ['git'] + args,
            cwd=cwd or get_repo_dir(),
            capture_output=True, text=True, timeout=15,
        )
        if r.returncode == 0:
            return r.stdout.strip()
        _last_error = (r.stderr or r.stdout).strip()
        return None
    except Exception as e:
        _last_error = str(e)
        return None


def _pip(args, timeout=60):
    """Run a pip command using the same Python that's running acorn.
    This ensures we install into the correct venv/environment."""
    import sys
    try:
        r = subprocess.run(
            [sys.executable, '-m', 'pip'] + args,
            capture_output=True, text=True, timeout=timeout,
        )
        output = (r.stdout + '\n' + r.stderr).strip()
        return r.returncode == 0, output
    except Exception as e:
        return False, str(e)


def _fetch_github_json(path):
    """Fetch JSON from GitHub API. Returns parsed dict or None."""
    global _last_error
    import urllib.request
    url = f'{GITHUB_API}/{path}'
    try:
        req = urllib.request.Request(url, headers={'Accept': 'application/vnd.github.v3+json'})
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())
    except Exception as e:
        _last_error = str(e)
        return None


def get_current_version():
    """Get current version from __init__.py, pyproject.toml, or package metadata."""
    # First: __init__.py (always available, even in pip installs)
    try:
        from acorn import __version__
        if __version__ and __version__ != '?':
            return __version__
    except Exception:
        pass
    # Fallback: pyproject.toml (available in git clones)
    repo = get_repo_dir()
    try:
        with open(os.path.join(repo, 'pyproject.toml')) as f:
            for line in f:
                m = re.match(r'version\s*=\s*"(.+?)"', line)
                if m:
                    return m.group(1)
    except Exception:
        pass
    return '?'


def _get_local_commit():
    """Get local HEAD commit hash, or None if not a git repo."""
    if not is_git_repo():
        return None
    return _git(['rev-parse', '--short', 'HEAD'])


def check_for_updates():
    """Check if updates are available.

    Works in two modes:
    - git repo: fetch from origin, compare commits
    - pip install: compare local commit/version against GitHub API

    Returns dict with:
        available: bool
        local: str
        remote: str
        behind: int
        commits: list of (hash, message)
        method: 'git' or 'pip'
    Or None if check failed.
    """
    global _last_error
    _last_error = ''

    if is_git_repo():
        return _check_git()
    else:
        return _check_github_api()


def _check_git():
    """Git-based update check — fetch and compare."""
    repo = get_repo_dir()

    remote_url = _git(['remote', 'get-url', 'origin'], repo)
    if not remote_url:
        return None

    branch = _git(['rev-parse', '--abbrev-ref', 'HEAD'], repo)
    if not branch:
        return None

    fetch = _git(['fetch', 'origin', branch, '--quiet'], repo)
    if fetch is None:
        fetch = _git(['fetch', 'origin', '--quiet'], repo)
    if fetch is None:
        return None

    local = _git(['rev-parse', '--short', 'HEAD'], repo)
    remote = _git(['rev-parse', '--short', f'origin/{branch}'], repo)
    if not local or not remote:
        return None

    if local == remote:
        return {'available': False, 'local': local, 'remote': remote,
                'behind': 0, 'commits': [], 'method': 'git'}

    log = _git(['log', '--oneline', f'HEAD..origin/{branch}'], repo)
    commits = []
    if log:
        for line in log.strip().split('\n'):
            if line.strip():
                parts = line.split(' ', 1)
                commits.append((parts[0], parts[1] if len(parts) > 1 else ''))

    return {'available': True, 'local': local, 'remote': remote,
            'behind': len(commits), 'commits': commits, 'method': 'git'}


def _parse_version(v):
    """Parse '0.2.1' into a tuple (0, 2, 1) for comparison."""
    try:
        return tuple(int(x) for x in v.split('.'))
    except (ValueError, AttributeError):
        return (0,)


def _fetch_remote_version():
    """Fetch the version string from pyproject.toml on GitHub main branch."""
    import urllib.request
    import time as _time
    global _last_error
    # Cache-bust with timestamp to avoid stale raw.githubusercontent.com responses
    url = f'https://raw.githubusercontent.com/{GITHUB_REPO}/main/pyproject.toml?t={int(_time.time())}'
    try:
        req = urllib.request.Request(url, headers={'Cache-Control': 'no-cache'})
        with urllib.request.urlopen(req, timeout=10) as resp:
            content = resp.read().decode()
            m = re.search(r'version\s*=\s*"(.+?)"', content)
            return m.group(1) if m else None
    except Exception as e:
        _last_error = str(e)
        return None


def _check_github_api():
    """Pip-based update check — compare versions + show recent commits."""
    global _last_error

    local_version = get_current_version()
    remote_version = _fetch_remote_version()
    if not remote_version:
        return None

    # Only report update if remote is actually newer (not just different)
    if _parse_version(remote_version) <= _parse_version(local_version):
        return {'available': False, 'local': f'v{local_version}',
                'remote': f'v{remote_version}',
                'behind': 0, 'commits': [], 'method': 'pip'}

    # Fetch commits between versions — find the version bump commit
    data = _fetch_github_json('commits?per_page=20&sha=main')
    remote_commits = []
    if data and isinstance(data, list):
        for c in data:
            msg = (c.get('commit', {}).get('message', '') or '').split('\n')[0]
            sha = c['sha'][:7]
            # Stop at the commit that bumped to our current version
            if f'v{local_version}' in msg or f'{local_version}' in msg:
                break
            # Skip version bump commits themselves
            if msg.startswith('Bump version'):
                continue
            remote_commits.append((sha, msg))

    return {'available': True, 'local': f'v{local_version}',
            'remote': f'v{remote_version}',
            'behind': len(remote_commits) or 1, 'commits': remote_commits,
            'method': 'pip'}


def pull_update():
    """Pull latest changes via git. Returns (success, output)."""
    repo = get_repo_dir()
    branch = _git(['rev-parse', '--abbrev-ref', 'HEAD'], repo)
    if not branch:
        return False, 'Could not determine current branch'

    try:
        r = subprocess.run(
            ['git', 'pull', 'origin', branch],
            cwd=repo, capture_output=True, text=True, timeout=30,
        )
        output = (r.stdout + '\n' + r.stderr).strip()
        return r.returncode == 0, output
    except Exception as e:
        return False, str(e)


def pip_update():
    """Update via pip install from GitHub. Returns (success, output)."""
    url = f'git+https://github.com/{GITHUB_REPO}.git@main'
    ok, output = _pip(['install', '--upgrade', url, '--break-system-packages'])
    return ok, output


def reinstall():
    """Reinstall acorn-cli (editable if git repo, from GitHub otherwise)."""
    if is_git_repo():
        repo = get_repo_dir()
        ok, output = _pip(['install', '-e', repo, '--quiet', '--break-system-packages'])
        return ok, output
    else:
        return pip_update()
