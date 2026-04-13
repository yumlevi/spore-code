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


_env_cache = None


def gather_environment() -> str:
    """Detect hardware, installed tools, runtimes, and system info. Cached after first call."""
    global _env_cache
    if _env_cache is not None:
        return _env_cache

    parts = []

    # ── System ──
    parts.append(f'OS: {platform.system()} {platform.release()} ({platform.machine()})')
    shell = os.environ.get('SHELL', '')
    if shell:
        parts.append(f'Shell: {shell}')

    # ── CPU ──
    try:
        cpu_count = os.cpu_count() or 0
        parts.append(f'CPU: {cpu_count} cores')
        # Try to get CPU model
        cpu_model = _run("cat /proc/cpuinfo 2>/dev/null | grep 'model name' | head -1 | cut -d: -f2")
        if not cpu_model:
            cpu_model = _run("sysctl -n machdep.cpu.brand_string 2>/dev/null")
        if cpu_model:
            parts.append(f'CPU model: {cpu_model.strip()[:80]}')
    except Exception:
        pass

    # ── Memory ──
    try:
        mem = _run("free -h 2>/dev/null | grep Mem | awk '{print $2}'")
        if not mem:
            mem = _run("sysctl -n hw.memsize 2>/dev/null")
            if mem:
                mem = f'{int(mem) // (1024**3)}Gi'
        if mem:
            parts.append(f'RAM: {mem}')
    except Exception:
        pass

    # ── GPU / CUDA ──
    gpu_info = []
    # NVIDIA
    nvidia = _run("nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits 2>/dev/null")
    if nvidia:
        for line in nvidia.strip().split('\n'):
            gpu_info.append(f'  NVIDIA: {line.strip()}')
        cuda_version = _run("nvcc --version 2>/dev/null | grep release | awk '{print $5}' | tr -d ','")
        if cuda_version:
            gpu_info.append(f'  CUDA: {cuda_version}')
        else:
            cuda_version = _run("nvidia-smi 2>/dev/null | grep 'CUDA Version' | awk '{print $9}'")
            if cuda_version:
                gpu_info.append(f'  CUDA (driver): {cuda_version}')
    # ROCm / AMD
    rocm = _run("rocm-smi --showproductname 2>/dev/null | head -5")
    if rocm and 'ERROR' not in rocm:
        gpu_info.append(f'  AMD ROCm: {rocm.split(chr(10))[0][:60]}')
    # Apple Metal
    if platform.system() == 'Darwin':
        metal = _run("system_profiler SPDisplaysDataType 2>/dev/null | grep 'Chipset Model'")
        if metal:
            gpu_info.append(f'  Apple GPU: {metal.split(":")[-1].strip()[:60]}')

    if gpu_info:
        parts.append('GPU:\n' + '\n'.join(gpu_info))
    else:
        parts.append('GPU: none detected')

    # ── Disk ──
    try:
        disk = _run("df -h . 2>/dev/null | tail -1 | awk '{print $4 \" available of \" $2}'")
        if disk:
            parts.append(f'Disk: {disk}')
    except Exception:
        pass

    # ── Tools / Runtimes ──
    tools = {
        'node': 'node --version',
        'npm': 'npm --version',
        'yarn': 'yarn --version',
        'pnpm': 'pnpm --version',
        'bun': 'bun --version',
        'deno': 'deno --version 2>&1 | head -1',
        'python3': 'python3 --version',
        'pip': 'pip3 --version',
        'uv': 'uv --version',
        'go': 'go version',
        'rust/cargo': 'cargo --version',
        'java': 'java --version 2>&1 | head -1',
        'dotnet': 'dotnet --version',
        'ruby': 'ruby --version',
        'php': 'php --version 2>&1 | head -1',
        'docker': 'docker --version',
        'docker-compose': 'docker compose version 2>/dev/null || docker-compose --version 2>/dev/null',
        'kubectl': 'kubectl version --client --short 2>/dev/null',
        'terraform': 'terraform --version 2>&1 | head -1',
        'git': 'git --version',
        'make': 'make --version 2>&1 | head -1',
        'cmake': 'cmake --version 2>&1 | head -1',
        'gcc': 'gcc --version 2>&1 | head -1',
        'clang': 'clang --version 2>&1 | head -1',
        'ffmpeg': 'ffmpeg -version 2>&1 | head -1',
        'sqlite3': 'sqlite3 --version',
        'redis-cli': 'redis-cli --version',
        'psql': 'psql --version',
        'mysql': 'mysql --version',
        'mongosh': 'mongosh --version 2>/dev/null',
        'nginx': 'nginx -v 2>&1',
    }

    available = []
    missing_important = []
    # Only report missing for commonly expected tools
    important = {'node', 'npm', 'python3', 'git', 'docker', 'make', 'gcc'}

    for name, cmd in tools.items():
        binary = cmd.split()[0]
        if shutil.which(binary):
            version = _run(cmd)
            if version:
                available.append(f'  {name}: {version.split(chr(10))[0][:60]}')
            else:
                available.append(f'  {name}: installed')
        elif name in important:
            missing_important.append(name)

    if available:
        parts.append('Installed tools:\n' + '\n'.join(available))
    if missing_important:
        parts.append(f'Not installed: {", ".join(missing_important)}')

    _env_cache = '\n'.join(parts)
    return _env_cache


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
