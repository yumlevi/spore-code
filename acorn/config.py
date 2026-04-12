"""Configuration loading and first-run setup wizard."""

import os
import sys
from pathlib import Path

CONFIG_DIR = Path.home() / '.acorn'
CONFIG_FILE = CONFIG_DIR / 'config.toml'
LAST_SESSION_FILE = CONFIG_DIR / 'last_session'


def save_last_session(session_id: str, cwd: str):
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    LAST_SESSION_FILE.write_text(f'{session_id}\n{cwd}\n')


def load_last_session():
    if not LAST_SESSION_FILE.exists():
        return None, None
    parts = LAST_SESSION_FILE.read_text().strip().split('\n')
    if len(parts) >= 2:
        return parts[0], parts[1]
    return None, None


def load_config() -> "dict | None":
    if not CONFIG_FILE.exists():
        return None
    try:
        import tomllib
    except ImportError:
        import tomli as tomllib
    return tomllib.loads(CONFIG_FILE.read_text())


def save_config(cfg: dict):
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    lines = ['[connection]']
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
    CONFIG_FILE.write_text('\n'.join(lines) + '\n')


def run_setup_wizard() -> dict:
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
    cfg = {
        'connection': {'host': host, 'port': port, 'user': user, 'key': key},
        'display': {'theme': 'dark', 'show_thinking': True, 'show_tools': True, 'show_usage': True},
    }
    save_config(cfg)
    print(f'\nConfig saved to {CONFIG_FILE}\n')
    return cfg
