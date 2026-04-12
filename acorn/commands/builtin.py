"""Built-in slash commands."""

import os
from acorn.commands.registry import command
from acorn.context import gather_context, _tree
from acorn.protocol import clear_message, stop_message
from acorn.themes import get_theme, list_themes
from rich.panel import Panel


@command('/help')
async def cmd_help(args, **ctx):
    ctx['renderer'].console.print(Panel(
        '/help            Show this help\n'
        '/quit            Exit Acorn\n'
        '/clear           Clear session history\n'
        '/stop            Abort current generation\n'
        '/plan            Toggle plan mode (research only, no changes)\n'
        '/status          Connection info\n'
        '/context         Show project context\n'
        '/tree [depth]    Show project tree\n'
        '/theme [name]    Switch theme (dark, light, oak, forest)\n'
        '/init            Create ACORN.md template\n'
        '/approve-all     Auto-approve all tools',
        title='Acorn Commands',
    ))


@command('/quit', '/exit')
async def cmd_quit(args, **ctx):
    return 'quit'


@command('/clear')
async def cmd_clear(args, **ctx):
    await ctx['conn'].send(clear_message(ctx['session_id']))
    ctx['state']['context_sent'] = False
    ctx['renderer'].show_info('Session cleared')


@command('/stop')
async def cmd_stop(args, **ctx):
    await ctx['conn'].send(stop_message(ctx['session_id']))
    ctx['renderer'].show_info('Stop requested')


@command('/status')
async def cmd_status(args, **ctx):
    ctx['renderer'].console.print(
        f'[bold]Acorn Status[/bold]\n'
        f'User:    {ctx["user"]}\n'
        f'Session: {ctx["session_id"]}\n'
        f'Server:  {ctx["conn"].host}:{ctx["conn"].port}\n'
        f'CWD:     {os.getcwd()}'
    )


@command('/context')
async def cmd_context(args, **ctx):
    context = gather_context(os.getcwd())
    ctx['renderer'].console.print(Panel(context, title='Project Context'))
    if args.strip() == 'refresh':
        ctx['state']['context_sent'] = False
        ctx['renderer'].show_info('Context will be re-sent on next message')


@command('/tree')
async def cmd_tree(args, **ctx):
    depth = int(args.strip()) if args.strip().isdigit() else 3
    tree = _tree(os.getcwd(), max_depth=depth, max_entries=100)
    ctx['renderer'].console.print(tree)


@command('/init')
async def cmd_init(args, **ctx):
    cwd = os.getcwd()
    path = os.path.join(cwd, 'ACORN.md')
    if os.path.exists(path):
        ctx['renderer'].console.print('[yellow]ACORN.md already exists[/yellow]')
        return
    with open(path, 'w') as f:
        f.write(
            '# Project Instructions for Acorn\n\n'
            '<!-- Add project-specific context here. Acorn sends this to the agent. -->\n\n'
            '## Overview\n\n## Conventions\n\n## Important files\n'
        )
    ctx['renderer'].console.print('[green]Created ACORN.md[/green]')
    # Add .acorn/ to .gitignore if it exists
    gitignore = os.path.join(cwd, '.gitignore')
    if os.path.exists(gitignore):
        content = open(gitignore).read()
        if '.acorn/' not in content:
            with open(gitignore, 'a') as f:
                f.write('\n# Acorn local data\n.acorn/\n')
            ctx['renderer'].show_info('Added .acorn/ to .gitignore')


@command('/approve-all')
async def cmd_approve_all(args, **ctx):
    ctx['executor'].permissions.approve_all = True
    ctx['renderer'].console.print('[yellow]All tool executions will be auto-approved[/yellow]')


@command('/plan')
async def cmd_plan(args, **ctx):
    ctx['state']['plan_mode'] = not ctx['state'].get('plan_mode', False)
    if ctx['state']['plan_mode']:
        ctx['renderer'].console.print('[cyan]Plan mode ON[/cyan] — agent will research and plan but not execute changes')
    else:
        ctx['renderer'].console.print('[cyan]Plan mode OFF[/cyan] — agent will execute normally')


@command('/theme')
async def cmd_theme(args, **ctx):
    name = args.strip()
    available = list_themes()
    if not name:
        current = ctx['renderer'].theme.get('name', 'dark')
        ctx['renderer'].console.print(f'Current theme: [bold]{current}[/bold]')
        ctx['renderer'].console.print(f'Available: {", ".join(available)}')
        ctx['renderer'].console.print('Usage: /theme <name>')
        return
    if name not in available:
        ctx['renderer'].show_error(f'Unknown theme: {name}. Available: {", ".join(available)}')
        return
    ctx['renderer'].theme = get_theme(name)
    ctx['renderer'].console.print(f'Theme changed to [bold]{name}[/bold]')
