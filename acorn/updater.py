"""Auto-updater — checks git remote for new commits, notifies user, handles install."""

import os
import subprocess


def get_repo_dir():
    """Get the acorn-cli repo directory."""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _git(args, cwd=None):
    """Run a git command and return stdout, or None on failure."""
    try:
        r = subprocess.run(
            ['git'] + args,
            cwd=cwd or get_repo_dir(),
            capture_output=True, text=True, timeout=15,
        )
        return r.stdout.strip() if r.returncode == 0 else None
    except Exception:
        return None


def check_for_updates():
    """Check if there are new commits on the remote.

    Returns dict with:
        available: bool
        local: str (short hash)
        remote: str (short hash)
        behind: int (number of commits behind)
        commits: list of (hash, message) tuples
    Or None if check failed.
    """
    repo = get_repo_dir()

    # Get current branch
    branch = _git(['rev-parse', '--abbrev-ref', 'HEAD'], repo)
    if not branch:
        return None

    # Fetch latest from remote (quiet, no tags)
    fetch = _git(['fetch', 'origin', branch, '--quiet'], repo)
    if fetch is None:
        return None

    local = _git(['rev-parse', '--short', 'HEAD'], repo)
    remote = _git(['rev-parse', '--short', f'origin/{branch}'], repo)

    if not local or not remote:
        return None

    if local == remote:
        return {'available': False, 'local': local, 'remote': remote, 'behind': 0, 'commits': []}

    # Count commits behind
    log = _git(['log', '--oneline', f'HEAD..origin/{branch}'], repo)
    commits = []
    if log:
        for line in log.strip().split('\n'):
            if line.strip():
                parts = line.split(' ', 1)
                commits.append((parts[0], parts[1] if len(parts) > 1 else ''))

    return {
        'available': True,
        'local': local,
        'remote': remote,
        'behind': len(commits),
        'commits': commits,
    }


def pull_update():
    """Pull latest changes. Returns (success, output)."""
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


def reinstall():
    """Reinstall acorn-cli in editable mode. Returns (success, output)."""
    repo = get_repo_dir()
    try:
        r = subprocess.run(
            ['pip', 'install', '-e', '.', '--quiet', '--break-system-packages'],
            cwd=repo, capture_output=True, text=True, timeout=60,
        )
        output = (r.stdout + '\n' + r.stderr).strip()
        if r.returncode != 0:
            # Try pip3
            r = subprocess.run(
                ['pip3', 'install', '-e', '.', '--quiet', '--break-system-packages'],
                cwd=repo, capture_output=True, text=True, timeout=60,
            )
            output = (r.stdout + '\n' + r.stderr).strip()
        return r.returncode == 0, output
    except Exception as e:
        return False, str(e)


def get_current_version():
    """Get current version from pyproject.toml."""
    import re
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
