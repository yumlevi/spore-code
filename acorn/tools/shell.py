"""Local shell command execution with background support + sandbox."""

import asyncio
import os
import re


DANGEROUS_PATTERNS = [
    'rm -rf /', 'mkfs', '> /dev/sd', ':(){:|:&};:', 'chmod -R 777 /',
]

# Known-safe commands that don't need extra approval in auto mode
SAFE_COMMANDS = {
    'ls', 'cat', 'head', 'tail', 'wc', 'sort', 'uniq', 'grep', 'find', 'which',
    'echo', 'pwd', 'whoami', 'date', 'uname', 'env', 'printenv', 'id',
    'git', 'node', 'npm', 'npx', 'yarn', 'pnpm', 'bun', 'deno',
    'python', 'python3', 'pip', 'pip3', 'uv',
    'go', 'cargo', 'rustc', 'java', 'javac', 'mvn', 'gradle',
    'ruby', 'gem', 'php', 'composer',
    'docker', 'kubectl', 'terraform',
    'make', 'cmake', 'gcc', 'g++', 'clang',
    'curl', 'wget', 'ssh', 'scp', 'rsync',
    'tar', 'zip', 'unzip', 'gzip', 'gunzip',
    'sed', 'awk', 'cut', 'tr', 'diff', 'patch',
    'mkdir', 'touch', 'cp', 'mv', 'ln',
    'chmod', 'chown',
    'sqlite3', 'psql', 'mysql', 'redis-cli', 'mongosh',
    'ffmpeg', 'convert', 'jq', 'yq',
    'nginx', 'systemctl', 'journalctl',
    'tree', 'file', 'stat', 'du', 'df', 'free', 'top', 'htop', 'ps',
    'nvidia-smi', 'rocm-smi',
    'tsc', 'eslint', 'prettier', 'jest', 'vitest', 'pytest', 'mocha',
    'kill',  # SIGTERM is fine, kill -9 caught by permissions
    'seq', 'sleep', 'true', 'false', 'test',
    'sh', 'bash', 'zsh',
}

# Paths that commands should not reference
BLOCKED_PATHS = [
    '/etc/shadow', '/etc/passwd-', '/etc/sudoers',
    '~/.ssh/id_', '~/.ssh/authorized_keys',
    '~/.gnupg', '~/.aws/credentials', '~/.kube/config',
]

def _build_blocked_patterns():
    patterns = []
    for p in BLOCKED_PATHS:
        # Match both ~ and expanded home
        patterns.append(re.compile(re.escape(p)))
        expanded = p.replace('~', os.path.expanduser('~'))
        if expanded != p:
            patterns.append(re.compile(re.escape(expanded)))
    return patterns

BLOCKED_PATH_RE = _build_blocked_patterns()

# Commands that are likely long-running (servers, watchers, etc.)
BACKGROUND_HINTS = [
    'npm start', 'npm run dev', 'npm run serve', 'yarn start', 'yarn dev',
    'python -m http.server', 'python manage.py runserver',
    'node server', 'nodemon', 'next dev', 'vite',
    'flask run', 'uvicorn', 'gunicorn', 'cargo run',
    'docker compose up', 'docker-compose up',
    'tail -f', 'watch ',
]


def get_command_binary(command: str) -> str:
    """Extract the first binary/command from a shell command string."""
    # Handle cd, env prefix, sudo, etc.
    cmd = command.strip()
    for prefix in ('sudo ', 'env ', 'nice ', 'nohup ', 'time '):
        if cmd.startswith(prefix):
            cmd = cmd[len(prefix):]
    # Handle VAR=val prefix
    while '=' in cmd.split()[0] if cmd.split() else False:
        cmd = cmd.split(None, 1)[1] if ' ' in cmd else cmd
    binary = cmd.split()[0] if cmd.split() else ''
    # Strip path
    if '/' in binary:
        binary = binary.rsplit('/', 1)[-1]
    return binary


def check_path_safety(command: str) -> str:
    """Check if command references sensitive paths. Returns error string or empty."""
    for r in BLOCKED_PATH_RE:
        if r.search(command):
            return f'Command references sensitive path: {r.pattern}'
    return ''


async def execute(input: dict, cwd: str, process_manager=None) -> dict:
    command = input.get('command', '')
    timeout_ms = min(input.get('timeout', 120000), 600000)
    timeout = timeout_ms / 1000
    background = input.get('background', False)

    for pattern in DANGEROUS_PATTERNS:
        if pattern in command:
            return {'error': f'Blocked dangerous command pattern: {pattern}'}

    # Check sensitive path access
    path_err = check_path_safety(command)
    if path_err:
        return {'error': path_err}

    # Auto-detect likely long-running commands → suggest background
    is_server_like = any(hint in command for hint in BACKGROUND_HINTS)

    # If explicitly background or detected as server-like with a process manager
    if (background or is_server_like) and process_manager:
        bp = await process_manager.launch(command, cwd)
        # Wait briefly for early output or crash
        await asyncio.sleep(1.0)
        early_output = '\n'.join(bp.output) if bp.output else '(started, no output yet)'
        if not bp.running:
            return {
                'output': early_output,
                'exitCode': bp.exit_code,
                'note': f'Process exited immediately (exit {bp.exit_code})',
            }
        return {
            'output': early_output[:2000],
            'backgrounded': True,
            'processId': bp.id,
            'note': f'Running in background as #{bp.id}. Use /bg {bp.id} to view output, /bg kill {bp.id} to stop.',
        }

    try:
        proc = await asyncio.create_subprocess_shell(
            command, cwd=cwd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
        )
        stdout, _ = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        output = stdout.decode('utf-8', errors='replace')
        if len(output) > 8000:
            mid = len(output) - 8000
            output = output[:4000] + f'\n\n[... {mid} chars truncated ...]\n\n' + output[-4000:]
        return {'output': output, 'exitCode': proc.returncode}
    except asyncio.TimeoutError:
        # On timeout, move to background instead of killing if we have a process manager
        if process_manager:
            bp = await process_manager.launch(command, cwd)
            try:
                proc.kill()
            except Exception:
                pass
            return {
                'output': f'Command timed out after {timeout}s — moved to background as #{bp.id}',
                'backgrounded': True,
                'processId': bp.id,
                'note': f'Use /bg {bp.id} to view output, /bg kill {bp.id} to stop.',
            }
        try:
            proc.kill()
        except Exception:
            pass
        return {'error': f'Command timed out after {timeout}s', 'exitCode': -1}
    except Exception as e:
        return {'error': str(e)}
