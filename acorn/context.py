"""Gather local project context to send with messages."""

import os
import platform
import shutil
import subprocess
from acorn.session import find_git_root


def _run(cmd: str, cwd: str = None) -> str:
    try:
        r = subprocess.run(
            cmd, shell=True, cwd=cwd,
            capture_output=True, text=True, timeout=5,
        )
        return r.stdout.strip() if r.returncode == 0 else ''
    except Exception:
        return ''


def _git(cmd: str, cwd: str) -> str:
    return _run(f'git {cmd}', cwd)


def _tree(root: str, max_depth: int = 2, max_entries: int = 50) -> str:
    entries = []
    for dirpath, dirnames, filenames in os.walk(root):
        depth = dirpath.replace(root, '').count(os.sep)
        if depth >= max_depth:
            dirnames.clear()
            continue
        # Skip hidden dirs and common noise
        dirnames[:] = [d for d in sorted(dirnames)
                       if not d.startswith('.') and d not in ('node_modules', '__pycache__', '.git', 'venv', '.venv')]
        indent = '  ' * depth
        basename = os.path.basename(dirpath) or os.path.basename(root)
        entries.append(f'{indent}{basename}/')
        for f in sorted(filenames)[:20]:
            if not f.startswith('.'):
                entries.append(f'{indent}  {f}')
        if len(entries) >= max_entries:
            entries.append(f'{indent}  ... (truncated)')
            break
    return '\n'.join(entries)


def gather_environment() -> str:
    """Detect installed tools, runtimes, and system info."""
    parts = []

    # OS
    parts.append(f'OS: {platform.system()} {platform.release()} ({platform.machine()})')

    # Shell
    shell = os.environ.get('SHELL', '')
    if shell:
        parts.append(f'Shell: {shell}')

    # Detect installed runtimes and tools
    tools = {
        'node': 'node --version',
        'npm': 'npm --version',
        'yarn': 'yarn --version',
        'pnpm': 'pnpm --version',
        'bun': 'bun --version',
        'python3': 'python3 --version',
        'pip': 'pip3 --version',
        'go': 'go version',
        'rust/cargo': 'cargo --version',
        'java': 'java --version 2>&1 | head -1',
        'docker': 'docker --version',
        'git': 'git --version',
        'make': 'make --version 2>&1 | head -1',
        'gcc': 'gcc --version 2>&1 | head -1',
    }

    available = []
    missing = []
    for name, cmd in tools.items():
        # Check if binary exists first (faster than running)
        binary = cmd.split()[0]
        if shutil.which(binary):
            version = _run(cmd)
            if version:
                available.append(f'  {name}: {version.split(chr(10))[0][:60]}')
            else:
                available.append(f'  {name}: installed')
        else:
            missing.append(name)

    if available:
        parts.append('Available tools:\n' + '\n'.join(available))
    if missing:
        parts.append(f'Not installed: {", ".join(missing)}')

    # Package manager lockfiles in cwd tell us about the project
    return '\n'.join(parts)


def detect_project_type(cwd: str) -> str:
    """Detect project type from files present."""
    indicators = []
    checks = {
        'package.json': 'Node.js',
        'tsconfig.json': 'TypeScript',
        'requirements.txt': 'Python (pip)',
        'pyproject.toml': 'Python (modern)',
        'Pipfile': 'Python (pipenv)',
        'go.mod': 'Go',
        'Cargo.toml': 'Rust',
        'pom.xml': 'Java (Maven)',
        'build.gradle': 'Java (Gradle)',
        'Gemfile': 'Ruby',
        'composer.json': 'PHP',
        'Dockerfile': 'Docker',
        'docker-compose.yml': 'Docker Compose',
        'Makefile': 'Make',
        '.github/workflows': 'GitHub Actions CI',
        'next.config.js': 'Next.js',
        'next.config.ts': 'Next.js',
        'vite.config.ts': 'Vite',
        'vite.config.js': 'Vite',
        'angular.json': 'Angular',
        'svelte.config.js': 'SvelteKit',
        'tailwind.config.js': 'Tailwind CSS',
        'tailwind.config.ts': 'Tailwind CSS',
    }
    for file, tech in checks.items():
        path = os.path.join(cwd, file)
        if os.path.exists(path):
            indicators.append(tech)
    return ', '.join(indicators) if indicators else 'Unknown'


def gather_context(cwd: str) -> str:
    git_root = find_git_root(cwd)
    project = os.path.basename(git_root or cwd)
    parts = [f'[Acorn Context — {project}]']
    parts.append(f'CWD: {cwd}')

    if git_root:
        branch = _git('branch --show-current', git_root)
        status = _git('status --short', git_root)
        log = _git('log --oneline -5', git_root)
        parts.append(f'Git: branch={branch}')
        if status:
            lines = status.split('\n')
            if len(lines) > 20:
                status = '\n'.join(lines[:20]) + f'\n... ({len(lines) - 20} more)'
            parts.append(f'Status:\n{status}')
        if log:
            parts.append(f'Recent commits:\n{log}')

    # Environment
    env = gather_environment()
    parts.append(f'Environment:\n{env}')

    # Project type detection
    proj_type = detect_project_type(git_root or cwd)
    if proj_type != 'Unknown':
        parts.append(f'Detected project type: {proj_type}')

    # ACORN.md
    acorn_md = os.path.join(git_root or cwd, 'ACORN.md')
    if os.path.exists(acorn_md):
        try:
            content = open(acorn_md).read()[:4000]
            parts.append(f'--- ACORN.md ---\n{content}\n--- end ---')
        except Exception:
            pass

    # Directory tree
    tree = _tree(git_root or cwd, max_depth=2, max_entries=50)
    if tree:
        parts.append(f'Project tree:\n{tree}')

    return '\n\n'.join(parts)
