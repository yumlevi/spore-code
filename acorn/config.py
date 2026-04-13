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


VALID_KEYS = {
    'connection': {'host', 'port', 'user', 'key'},
    'display': {'theme', 'show_thinking', 'show_tools', 'show_usage'},
}


def _validate_config(cfg: dict):
    """Warn about unknown keys in config — catches typos."""
    import difflib
    warnings = []
    for section, keys in cfg.items():
        if section not in VALID_KEYS:
            close = difflib.get_close_matches(section, VALID_KEYS.keys(), n=1)
            hint = f' (did you mean "{close[0]}"?)' if close else ''
            warnings.append(f'Unknown section [{section}]{hint}')
            continue
        if isinstance(keys, dict):
            for key in keys:
                if key not in VALID_KEYS[section]:
                    close = difflib.get_close_matches(key, VALID_KEYS[section], n=1)
                    hint = f' (did you mean "{close[0]}"?)' if close else ''
                    warnings.append(f'Unknown key [{section}].{key}{hint}')
    if warnings:
        import sys
        for w in warnings:
            print(f'⚠ Config: {w}', file=sys.stderr)


def load_config() -> "dict | None":
    if not GLOBAL_CONFIG.exists():
        return None
    cfg = _parse_toml(GLOBAL_CONFIG)
    if cfg:
        _validate_config(cfg)
    return cfg


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
    from rich.console import Console
    from rich.panel import Panel
    from rich.text import Text
    from rich.prompt import Prompt, Confirm
    from rich.rule import Rule
    from rich.table import Table
    from acorn.themes import list_themes, get_theme

    console = Console()

    # Banner
    logo = Text()
    logo.append(r"""
     ██████╗  ██████╗ ██████╗ ██████╗ ███╗   ██╗
    ██╔══██╗██╔════╝██╔═══██╗██╔══██╗████╗  ██║
    ███████║██║     ██║   ██║██████╔╝██╔██╗ ██║
    ██╔══██║██║     ██║   ██║██╔══██╗██║╚██╗██║
    ██║  ██║╚██████╗╚██████╔╝██║  ██║██║ ╚████║
    ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝
""", style='bold cyan')
    console.print(logo)
    console.print(Panel(
        '[bold]Welcome to Acorn[/bold]\n'
        'CLI coding assistant powered by Anima',
        border_style='cyan',
        padding=(0, 2),
    ))
    console.print()

    # Step 1: Connection
    console.print(Rule('[bold cyan]1. Connect to Anima[/bold cyan]', style='dim'))
    console.print()
    console.print('  [dim]Enter your Anima server address.[/dim]')
    console.print('  [dim]Examples: 192.168.1.78 · https://acorn.example.com[/dim]')
    console.print()
    host = Prompt.ask('  [bold]Host[/bold]', default='localhost', console=console)

    port = 18810
    if '://' not in host:
        port_str = Prompt.ask('  [bold]Port[/bold]', default='18810', console=console)
        try:
            port = int(port_str)
        except ValueError:
            port = 18810
    console.print()

    # Step 2: Identity
    console.print(Rule('[bold cyan]2. Your identity[/bold cyan]', style='dim'))
    console.print()
    console.print('  [dim]Choose a username — the agent will remember you by this name.[/dim]')
    console.print()
    user = ''
    while not user:
        user = Prompt.ask('  [bold]Username[/bold]', console=console).strip()
        if not user:
            console.print('  [red]Username is required.[/red]')
    console.print()

    # Step 3: Team key
    console.print(Rule('[bold cyan]3. Authentication[/bold cyan]', style='dim'))
    console.print()
    console.print('  [dim]Enter the team key from your Anima server\'s .env file[/dim]')
    console.print('  [dim](ANIMA_ACORN_KEY value)[/dim]')
    console.print()
    key = ''
    while not key:
        key = Prompt.ask('  [bold]Team key[/bold]', console=console).strip()
        if not key:
            console.print('  [red]Team key is required.[/red]')
    console.print()

    # Step 4: Test connection
    console.print(Rule('[bold cyan]4. Testing connection[/bold cyan]', style='dim'))
    console.print()
    from acorn.connection import Connection, AuthError
    import asyncio
    conn = Connection(host, port)
    try:
        loop = asyncio.new_event_loop()
        token = loop.run_until_complete(conn.authenticate(user, key))
        loop.close()
        console.print('  [bold green]✓[/bold green] Connected and authenticated successfully!')
    except AuthError as e:
        console.print(f'  [bold red]✗[/bold red] Authentication failed: {e}')
        console.print('  [dim]Check your team key and try again.[/dim]')
        if not Confirm.ask('  Continue anyway?', default=False, console=console):
            sys.exit(1)
    except Exception as e:
        console.print(f'  [bold red]✗[/bold red] Cannot reach server: {e}')
        console.print('  [dim]Check the host/port and make sure Anima is running.[/dim]')
        if not Confirm.ask('  Continue anyway?', default=False, console=console):
            sys.exit(1)
    console.print()

    # Step 5: Theme
    console.print(Rule('[bold cyan]5. Choose a theme[/bold cyan]', style='dim'))
    console.print()
    themes = list_themes()
    theme_table = Table.grid(padding=(0, 3))
    for name in themes:
        td = get_theme(name)
        row = Text()
        row.append(f'  {td.get("icon", "?")} ', style='')
        row.append(f'{name:8s}', style=f'bold {td["accent"]}')
        row.append(f'  {td["bg"]}', style=td.get('muted', 'dim'))
        theme_table.add_row(row)
    console.print(theme_table)
    console.print()
    theme = Prompt.ask('  [bold]Theme[/bold]', default=themes[0], choices=themes, console=console)
    console.print()

    # Save
    cfg = {
        'connection': {'host': host, 'port': port, 'user': user, 'key': key},
        'display': {'theme': theme, 'show_thinking': True, 'show_tools': True, 'show_usage': True},
    }
    save_config(cfg)

    console.print(Rule(style='dim'))
    console.print()
    console.print(Panel(
        f'[bold green]✓ Setup complete![/bold green]\n\n'
        f'  Config saved to [cyan]{GLOBAL_CONFIG}[/cyan]\n'
        f'  User: [bold]{user}[/bold]\n'
        f'  Server: [bold]{host}:{port}[/bold]\n'
        f'  Theme: [bold]{theme}[/bold]\n\n'
        f'[dim]Run [bold]acorn[/bold] to start · [bold]acorn --help[/bold] for options[/dim]',
        border_style='green',
        padding=(1, 2),
    ))
    console.print()
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
