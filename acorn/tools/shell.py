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


def _handle_bg_command(args: str, pm) -> dict:
    """Handle /bg subcommands: list, read <id>, kill <id>."""
    if not args or args == 'list':
        procs = pm.list_all()
        if not procs:
            return {'output': 'No background processes'}
        lines = []
        for bp in procs:
            status = 'running' if bp.running else f'exited ({bp.exit_code})'
            lines.append(f'#{bp.id}  {status}  {bp.elapsed}  {bp.command[:80]}')
        return {'output': '\n'.join(lines)}

    parts = args.split(None, 1)
    subcmd = parts[0]

    if subcmd == 'kill' and len(parts) > 1:
        try:
            pid = int(parts[1])
        except ValueError:
            return {'error': f'Invalid process ID: {parts[1]}'}
        if pm.kill(pid):
            return {'output': f'Killed #{pid}'}
        return {'error': f'Process #{pid} not found or already stopped'}

    # Default: treat as process ID to read output
    try:
        pid = int(subcmd)
    except ValueError:
        return {'error': f'Usage: /bg [list | <id> | kill <id>]'}

    bp = pm.get(pid)
    if not bp:
        return {'error': f'Process #{pid} not found'}

    output_lines = list(bp.output)
    status = 'running' if bp.running else f'exited ({bp.exit_code})'
    header = f'#{bp.id} [{status}] {bp.elapsed} — {bp.command[:80]}'
    if not output_lines:
        return {'output': f'{header}\n(no output captured)'}
    body = '\n'.join(output_lines[-100:])
    if len(output_lines) > 100:
        body = f'... ({len(output_lines) - 100} earlier lines)\n{body}'
    return {'output': f'{header}\n{body}'}


async def execute(input: dict, cwd: str, process_manager=None) -> dict:
    command = input.get('command', '')
    timeout_ms = min(input.get('timeout', 120000), 600000)
    timeout = timeout_ms / 1000
    background = input.get('background', False)

    # Intercept background process commands — agent can read/list/kill bg processes
    bg_match = re.match(r'^/bg\s*(.*)$', command.strip())
    if bg_match and process_manager:
        return _handle_bg_command(bg_match.group(1).strip(), process_manager)

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
        # Wait for early output or crash — longer wait to capture server startup
        await asyncio.sleep(3.0)
        early_output = '\n'.join(bp.output) if bp.output else '(started, no output yet)'
        if not bp.running:
            return {
                'output': early_output,
                'exitCode': bp.exit_code,
                'note': f'Process exited immediately (exit {bp.exit_code})',
            }
        return {
            'output': early_output[:4000],
            'backgrounded': True,
            'processId': bp.id,
            'note': f'Running in background as #{bp.id}. To read latest output: exec /bg {bp.id}. To kill: exec /bg kill {bp.id}. To list all: exec /bg list.',
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
                'note': f'To read latest output: exec /bg {bp.id}. To kill: exec /bg kill {bp.id}.',
            }
        try:
            proc.kill()
        except Exception:
            pass
        return {'error': f'Command timed out after {timeout}s', 'exitCode': -1}
    except Exception as e:
        return {'error': str(e)}
