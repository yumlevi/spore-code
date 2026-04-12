"""Configuration — global (~/.acorn/) and local (.acorn/ per project)."""

import os
import sys
from pathlib import Path

# ── Global config (user home) ──────────────────────────────────────
GLOBAL_DIR = Path.home() / '.acorn'
GLOBAL_CONFIG = GLOBAL_DIR / 'config.toml'
GLOBAL_HISTORY = GLOBAL_DIR / 'history'
LAST_SESSION_FILE = GLOBAL_DIR / 'last_session'


def _parse_toml(path):
    try:
        import tomllib
    except ImportError:
        import tomli as tomllib
    return tomllib.loads(path.read_text())


def load_config() -> "dict | None":
    if not GLOBAL_CONFIG.exists():
        return None
    return _parse_toml(GLOBAL_CONFIG)


def save_config(cfg: dict):
    GLOBAL_DIR.mkdir(parents=True, exist_ok=True)
    lines = []

    lines.append('[connection]')
    conn = cfg.get('connection', {})
    lines.append(f'host = "{conn.get("host", "localhost")}"')
    lines.append(f'port = {conn.get("port", 18810)}')
    lines.append(f'user = "{conn.get("user", "")}"')
    lines.append(f'key = "{conn.get("key", "")}"')

    lines.append('')
    lines.append('[display]')
    disp = cfg.get('display', {})
    lines.append(f'theme = "{disp.get("theme", "dark")}"')
    lines.append(f'show_thinking = {str(disp.get("show_thinking", True)).lower()}')
    lines.append(f'show_tools = {str(disp.get("show_tools", True)).lower()}')
    lines.append(f'show_usage = {str(disp.get("show_usage", True)).lower()}')

    GLOBAL_CONFIG.write_text('\n'.join(lines) + '\n')


def run_setup_wizard() -> dict:
    from acorn.themes import list_themes
    print('Welcome to Acorn! Let\'s connect to your Anima agent.\n')
    host = input('Anima host [localhost]: ').strip() or 'localhost'
    port_str = input('Anima web port [18810]: ').strip() or '18810'
    port = int(port_str)
    user = input('Your username: ').strip()
    if not user:
        print('Username is required.')
        sys.exit(1)
    key = input('Team key (ANIMA_ACORN_KEY from server .env): ').strip()
    if not key:
        print('Team key is required.')
        sys.exit(1)
    themes = list_themes()
    print(f'\nAvailable themes: {", ".join(themes)}')
    theme = input(f'Theme [{themes[0]}]: ').strip() or themes[0]
    if theme not in themes:
        print(f'Unknown theme, using {themes[0]}')
        theme = themes[0]
    cfg = {
        'connection': {'host': host, 'port': port, 'user': user, 'key': key},
        'display': {'theme': theme, 'show_thinking': True, 'show_tools': True, 'show_usage': True},
    }
    save_config(cfg)
    print(f'\nConfig saved to {GLOBAL_CONFIG}\n')
    return cfg


# ── Session tracking ───────────────────────────────────────────────

def save_last_session(session_id: str, cwd: str):
    GLOBAL_DIR.mkdir(parents=True, exist_ok=True)
    LAST_SESSION_FILE.write_text(f'{session_id}\n{cwd}\n')


def load_last_session():
    if not LAST_SESSION_FILE.exists():
        return None, None
    parts = LAST_SESSION_FILE.read_text().strip().split('\n')
    if len(parts) >= 2:
        return parts[0], parts[1]
    return None, None


# ── Local project config (.acorn/ in cwd) ─────────────────────────

def local_dir(cwd: str) -> Path:
    """Get or create the .acorn/ directory in the project."""
    d = Path(cwd) / '.acorn'
    return d


def ensure_local_dir(cwd: str) -> Path:
    """Create .acorn/ with subdirs in the project."""
    d = local_dir(cwd)
    (d / 'plans').mkdir(parents=True, exist_ok=True)
    return d


def load_local_config(cwd: str) -> dict:
    """Load .acorn/config.toml from the project (overrides for this project)."""
    cfg_path = local_dir(cwd) / 'config.toml'
    if cfg_path.exists():
        return _parse_toml(cfg_path)
    return {}


def merged_config(cwd: str) -> dict:
    """Merge global + local config. Local overrides global."""
    cfg = load_config() or {}
    local = load_local_config(cwd)
    # Deep merge display section
    if 'display' in local:
        if 'display' not in cfg:
            cfg['display'] = {}
        cfg['display'].update(local['display'])
    # Connection can be overridden locally too
    if 'connection' in local:
        if 'connection' not in cfg:
            cfg['connection'] = {}
        cfg['connection'].update(local['connection'])
    return cfg
