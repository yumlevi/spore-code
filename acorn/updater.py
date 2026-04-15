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
    """Get current version from pyproject.toml."""
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


def _check_github_api():
    """Pip-based update check — compare against GitHub commits."""
    global _last_error

    # Get latest commits from GitHub
    data = _fetch_github_json('commits?per_page=10&sha=main')
    if not data or not isinstance(data, list):
        return None

    remote_hash = data[0]['sha'][:7] if data else '?'
    remote_commits = [(c['sha'][:7], (c.get('commit', {}).get('message', '') or '').split('\n')[0])
                      for c in data]

    # Try to find our local commit to see how far behind we are
    local_hash = _get_local_commit()
    if not local_hash:
        # No git — use version string as identifier
        local_hash = f'v{get_current_version()}'

    # Check if we're up to date by comparing hashes
    if local_hash == remote_hash:
        return {'available': False, 'local': local_hash, 'remote': remote_hash,
                'behind': 0, 'commits': [], 'method': 'pip'}

    # Find how far behind — look for our hash in the remote commits
    behind = 0
    new_commits = []
    for h, msg in remote_commits:
        if h == local_hash:
            break
        behind += 1
        new_commits.append((h, msg))

    if behind == 0:
        # Our hash wasn't in the last 10 commits — we're probably very behind
        behind = len(remote_commits)
        new_commits = remote_commits

    return {'available': True, 'local': local_hash, 'remote': remote_hash,
            'behind': behind, 'commits': new_commits, 'method': 'pip'}


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
