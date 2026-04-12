"""Acorn CLI — main entry point."""

import argparse
import asyncio
import json
import os
import sys

from rich.console import Console

from acorn.config import load_config, run_setup_wizard, save_last_session, load_last_session
from acorn.connection import Connection, AuthError
from acorn.context import gather_context
from acorn.permissions import Permissions
from acorn.protocol import chat_message
from acorn.renderer import Renderer
from acorn.session import compute_session_id, project_name, get_git_branch
from acorn.tools.executor import ToolExecutor
from acorn.commands.registry import get_command
import acorn.commands.builtin  # noqa: F401 — registers commands

PLAN_PREFIX = (
    '[MODE: Plan only. You are in planning mode.\n'
    'Phase 1 — UNDERSTAND: Read files, search the codebase, and use web_search/web_fetch as needed to fully understand the task. '
    'Ask the user clarifying questions if anything is ambiguous — do NOT assume.\n'
    'Phase 2 — PLAN: Once you have enough context, present a clear step-by-step plan of what you would change and why. '
    'Include file paths and describe each change.\n'
    'RULES: Do NOT make any changes (no write_file, edit_file, or exec). '
    'You MAY use read_file, glob, grep, web_search, and web_fetch.\n'
    'End your plan with the exact line: "PLAN_READY" on its own line so the client knows to prompt for approval.]\n\n'
)

PLAN_EXECUTE_MSG = (
    '[The user has approved the plan above. Switch to execute mode and implement it now. '
    'Proceed step by step, executing all the changes you outlined.]'
)


def _save_plan(cwd: str, plan_text: str) -> str:
    """Save an approved plan to .acorn/plans/ in the working directory."""
    import time
    plans_dir = os.path.join(cwd, '.acorn', 'plans')
    try:
        os.makedirs(plans_dir, exist_ok=True)
        ts = time.strftime('%Y%m%d-%H%M%S')
        filename = f'plan-{ts}.md'
        filepath = os.path.join(plans_dir, filename)
        # Strip the PLAN_READY marker
        clean = plan_text.replace('PLAN_READY', '').strip()
        with open(filepath, 'w') as f:
            f.write(f'# Plan — {ts}\n\n{clean}\n')
        return filepath
    except Exception:
        return None


async def prompt_plan_action(renderer) -> str:
    """Show arrow-key menu after a plan is presented. Returns 'execute', 'feedback', or 'cancel'."""
    from prompt_toolkit import Application
    from prompt_toolkit.key_binding import KeyBindings
    from prompt_toolkit.layout import Layout
    from prompt_toolkit.layout.containers import HSplit, Window
    from prompt_toolkit.layout.controls import FormattedTextControl

    choices = [
        ('execute', 'Execute plan'),
        ('feedback', 'Provide feedback'),
        ('cancel', 'Cancel'),
    ]
    selected = [0]
    result = [None]

    def get_text():
        lines = []
        lines.append(('bold', '\n  What would you like to do?\n\n'))
        for i, (key, label) in enumerate(choices):
            if i == selected[0]:
                lines.append(('bg:ansiblue fg:white bold', f'  ▸ {label}  '))
            else:
                lines.append(('', f'    {label}  '))
            lines.append(('', '\n'))
        lines.append(('dim', '\n  ↑↓ to select, Enter to confirm\n'))
        return lines

    kb = KeyBindings()

    @kb.add('up')
    def _up(event):
        selected[0] = (selected[0] - 1) % len(choices)

    @kb.add('down')
    def _down(event):
        selected[0] = (selected[0] + 1) % len(choices)

    @kb.add('enter')
    def _enter(event):
        result[0] = choices[selected[0]][0]
        event.app.exit()

    @kb.add('c-c')
    def _cancel(event):
        result[0] = 'cancel'
        event.app.exit()

    app = Application(
        layout=Layout(HSplit([Window(FormattedTextControl(get_text))])),
        key_bindings=kb,
        full_screen=False,
    )
    await app.run_async()
    return result[0] or 'cancel'


async def send_and_stream(conn, session_id, user, content, renderer):
    """Send a message and stream the response. Returns the final response text."""
    done_event = asyncio.Event()
    final_data = {}
    full_text = []

    async def on_start(msg):
        pass

    async def on_delta(msg):
        text = msg.get('text', '')
        full_text.append(text)
        renderer.stream_delta(text)

    async def on_status(msg):
        status = msg.get('status', '')
        if status == 'thinking_start':
            renderer.show_thinking()
        elif status == 'thinking':
            renderer.show_thinking(msg.get('tokens', 0))
        elif status == 'thinking_done':
            renderer.clear_thinking()
        elif status == 'tool_exec_start':
            renderer.show_tool_start(msg.get('tool', ''), msg.get('detail', ''))
        elif status == 'tool_exec_done':
            renderer.show_tool_done(msg.get('tool', ''), msg.get('resultChars', 0), msg.get('durationMs', 0))

    async def on_code_view(msg):
        renderer.show_code_view(msg.get('path', ''), msg.get('content', ''), msg.get('language', 'text'), msg.get('isNew', False))

    async def on_code_diff(msg):
        renderer.show_diff(msg.get('path', ''), msg.get('oldText', ''), msg.get('newText', ''))

    async def on_done(msg):
        final_data.update(msg)
        done_event.set()

    async def on_error(msg):
        renderer.show_error(msg.get('error', 'Unknown error'))
        done_event.set()

    async def on_tool(msg):
        pass  # tool name shown via status events

    conn.on('chat:start', on_start)
    conn.on('chat:delta', on_delta)
    conn.on('chat:status', on_status)
    conn.on('code:view', on_code_view)
    conn.on('code:diff', on_code_diff)
    conn.on('chat:done', on_done)
    conn.on('chat:error', on_error)
    conn.on('chat:tool', on_tool)

    renderer.start_streaming()
    await conn.send(chat_message(session_id, content, user))

    try:
        await asyncio.wait_for(done_event.wait(), timeout=600)
    except asyncio.TimeoutError:
        renderer.show_error('Response timed out (10 min)')

    renderer.finish_streaming(
        final_data.get('usage'),
        final_data.get('iterations'),
        final_data.get('toolUsage'),
    )

    return ''.join(full_text)


async def run_repl(conn, session_id, user, renderer, executor, initial_plan_mode=False):
    from prompt_toolkit import PromptSession
    from prompt_toolkit.history import FileHistory
    from prompt_toolkit.key_binding import KeyBindings
    from prompt_toolkit.formatted_text import FormattedText
    from pathlib import Path

    history_path = Path.home() / '.acorn' / 'history'
    history_path.parent.mkdir(parents=True, exist_ok=True)

    state = {'context_sent': False, 'plan_mode': initial_plan_mode}

    # Key bindings — Shift+Tab toggles plan/execute mode
    kb = KeyBindings()

    @kb.add('s-tab')
    def _toggle_plan(event):
        state['plan_mode'] = not state['plan_mode']
        mode = 'plan' if state['plan_mode'] else 'execute'
        # Force prompt refresh by invalidating the app
        event.app.invalidate()

    def _bottom_toolbar():
        if state['plan_mode']:
            return [('bg:ansiblue fg:white', ' [plan] research only — no changes '), ('', ' shift+tab to toggle ')]
        return [('bg:ansigreen fg:black', ' [execute] full agent mode '), ('', ' shift+tab to toggle ')]

    session = PromptSession(
        history=FileHistory(str(history_path)),
        key_bindings=kb,
        bottom_toolbar=_bottom_toolbar,
    )

    cwd = os.getcwd()
    proj = project_name(cwd)

    save_last_session(session_id, cwd)

    renderer.console.print(f'[bold]Acorn[/bold] connected as [cyan]{user}[/cyan] to [green]{proj}[/green]')
    renderer.console.print('[dim]Type /help for commands, Shift+Tab to toggle plan mode, /quit to exit[/dim]\n')

    while True:
        try:
            branch = get_git_branch(cwd) or ''
            prompt_text = f'{user}@{proj}'
            if branch:
                prompt_text += f' ({branch})'
            prompt_text += ' ❯ '

            text = await session.prompt_async(prompt_text)
            text = text.strip()
            if not text:
                continue

            # Slash commands
            if text.startswith('/'):
                parts = text.split(None, 1)
                cmd_name = parts[0]
                cmd_args = parts[1] if len(parts) > 1 else ''
                handler = get_command(cmd_name)
                if handler:
                    result = await handler(
                        cmd_args,
                        conn=conn, session_id=session_id, user=user,
                        renderer=renderer, executor=executor, state=state,
                    )
                    if result == 'quit':
                        break
                else:
                    renderer.show_error(f'Unknown command: {cmd_name}')
                continue

            # Enrich with context on first message
            content = text
            if not state['context_sent']:
                ctx = gather_context(cwd)
                content = ctx + '\n\n' + text
                state['context_sent'] = True

            # Plan mode prefix
            if state['plan_mode']:
                content = PLAN_PREFIX + content

            response_text = await send_and_stream(conn, session_id, user, content, renderer)

            # If in plan mode and plan looks complete, show action menu
            if state['plan_mode'] and response_text and ('PLAN_READY' in response_text or len(response_text) > 500):
                action = await prompt_plan_action(renderer)
                if action == 'execute':
                    # Save plan to .acorn/plans/ before executing
                    plan_path = _save_plan(cwd, response_text)
                    if plan_path:
                        renderer.show_info(f'Plan saved to {plan_path}')
                    state['plan_mode'] = False
                    renderer.console.print('[green]Executing plan...[/green]\n')
                    await send_and_stream(conn, session_id, user, PLAN_EXECUTE_MSG, renderer)
                elif action == 'feedback':
                    renderer.console.print('[dim]Type your feedback below — the agent will revise the plan.[/dim]')
                    # Stay in plan mode, next prompt will be feedback
                elif action == 'cancel':
                    renderer.console.print('[dim]Plan discarded.[/dim]')

        except KeyboardInterrupt:
            continue
        except EOFError:
            break


async def async_main(host, port, user, key, message=None, continue_session=False, plan_mode=False):
    console = Console()
    renderer = Renderer(console)

    # Authenticate
    conn = Connection(host, port)
    try:
        token = await conn.authenticate(user, key)
    except AuthError as e:
        renderer.show_error(f'Auth failed: {e}')
        return 1
    except Exception as e:
        renderer.show_error(f'Cannot reach server: {e}')
        return 1

    # Session — continue last or compute new
    cwd = os.getcwd()
    if continue_session:
        last_sid, last_cwd = load_last_session()
        if last_sid:
            session_id = last_sid
            renderer.show_info(f'Resuming session: {last_sid}')
            if last_cwd and last_cwd != cwd:
                renderer.show_info(f'Note: original dir was {last_cwd}')
        else:
            renderer.show_error('No previous session found')
            session_id = compute_session_id(user, cwd)
    else:
        session_id = compute_session_id(user, cwd)

    # Tools
    permissions = Permissions(renderer=renderer)
    executor = ToolExecutor(permissions, renderer, cwd)

    # Connect WebSocket
    try:
        await conn.connect(token)
    except Exception as e:
        renderer.show_error(f'WebSocket failed: {e}')
        return 1

    conn.tool_executor = executor

    try:
        if message:
            ctx = gather_context(cwd)
            content = ctx + '\n\n' + ' '.join(message)
            if plan_mode:
                content = PLAN_PREFIX + content
            save_last_session(session_id, cwd)
            await send_and_stream(conn, session_id, user, content, renderer)
        else:
            await run_repl(conn, session_id, user, renderer, executor, initial_plan_mode=plan_mode)
    finally:
        await conn.close()

    return 0


def main():
    parser = argparse.ArgumentParser(prog='acorn', description='CLI coding assistant connected to Anima')
    parser.add_argument('message', nargs='*', help='One-shot message (omit for REPL mode)')
    parser.add_argument('--host', help='Anima server host')
    parser.add_argument('--port', type=int, help='Anima web port')
    parser.add_argument('--user', help='Your username')
    parser.add_argument('-c', '--continue', dest='continue_session', action='store_true',
                        help='Resume the last session')
    parser.add_argument('--plan', action='store_true',
                        help='Plan mode — agent plans but does not execute')
    args = parser.parse_args()

    cfg = load_config()
    if not cfg:
        cfg = run_setup_wizard()

    host = args.host or cfg['connection']['host']
    port = args.port or cfg['connection']['port']
    user = args.user or cfg['connection']['user']
    key = cfg['connection']['key']

    exit_code = asyncio.run(async_main(
        host, port, user, key,
        message=args.message or None,
        continue_session=args.continue_session,
        plan_mode=args.plan,
    ))
    sys.exit(exit_code or 0)


if __name__ == '__main__':
    main()
