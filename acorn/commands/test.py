"""Test commands — /test <name> to exercise UI features locally without hitting the agent."""

import asyncio
import os
import json
from acorn.commands.registry import command
from acorn.questions import parse_questions, format_answers
from acorn.themes import get_theme, list_themes
from acorn.context import gather_environment, detect_project_type
from rich.text import Text
from rich.panel import Panel
from rich.markdown import Markdown
from rich.rule import Rule
from rich.table import Table


TESTS = {}


def test(name, description=''):
    def decorator(fn):
        TESTS[name] = {'fn': fn, 'desc': description}
        return fn
    return decorator


# ── Question parsing tests ─────────────────────────────────────────

@test('question-parse', 'Parse questions from sample agent text')
async def test_question_parse(app):
    t = app.theme_data

    # Test 1: Standard QUESTIONS block
    sample1 = """I need some clarification:

QUESTIONS:
1. What database? [PostgreSQL / MySQL / SQLite / MongoDB]
2. Which features? {Auth / API / WebSocket / Caching}
3. Expected user count?
4. Cloud provider? [AWS / GCP / Azure]
"""
    parsed1 = parse_questions(sample1)
    app._log(Text(f'  Test 1 — Standard block: parsed {len(parsed1)} questions', style=t['accent']))
    assert len(parsed1) == 4, f'Expected 4, got {len(parsed1)}'
    assert parsed1[0]['options'] == ['PostgreSQL', 'MySQL', 'SQLite', 'MongoDB']
    assert parsed1[1]['multi'] == True
    assert parsed1[2]['options'] is None  # open-ended
    app._log(Text('  ✓ Correct types: single, multi, open, single', style=t['success']))

    # Test 2: No QUESTIONS marker → should return empty
    sample2 = """Here's what I'll do:
1. Create the database schema
2. Set up the API routes
3. Add authentication
"""
    parsed2 = parse_questions(sample2)
    app._log(Text(f'  Test 2 — No marker: parsed {len(parsed2)} (expected 0)', style=t['accent']))
    assert len(parsed2) == 0, f'Expected 0, got {len(parsed2)}'
    app._log(Text('  ✓ No false positives from numbered lists', style=t['success']))

    # Test 3: Parentheses in text should NOT be parsed as options
    sample3 = """
QUESTIONS:
1. Do you want a Python-powered site (Flask/FastAPI)?
2. Which package manager? [npm / yarn / pnpm]
"""
    parsed3 = parse_questions(sample3)
    app._log(Text(f'  Test 3 — Parentheses: parsed {len(parsed3)} questions', style=t['accent']))
    assert len(parsed3) == 2
    assert parsed3[0]['options'] is None  # (Flask/FastAPI) not parsed as options
    assert parsed3[1]['options'] == ['npm', 'yarn', 'pnpm']
    app._log(Text('  ✓ Parentheses ignored, brackets work', style=t['success']))

    # Test 4: Multi-select with braces
    sample4 = """
QUESTIONS:
1. Select all that apply? {React / Vue / Svelte / Angular / Solid}
"""
    parsed4 = parse_questions(sample4)
    assert len(parsed4) == 1
    assert parsed4[0]['multi'] == True
    assert len(parsed4[0]['options']) == 5
    app._log(Text('  ✓ Multi-select with 5 options', style=t['success']))

    # Test 5: Questions block stops at blank line
    sample5 = """
QUESTIONS:
1. First? [A / B]
2. Second?

This is just regular text after the questions.
3. This should NOT be parsed.
"""
    parsed5 = parse_questions(sample5)
    assert len(parsed5) == 2, f'Expected 2, got {len(parsed5)}'
    app._log(Text('  ✓ Parsing stops at blank line', style=t['success']))

    app._log(Text(f'\n  All question parsing tests passed', style=f'bold {t["success"]}'))
    app._scroll_bottom()


@test('questions-inline', 'Interactive inline question flow')
async def test_questions_inline(app):
    t = app.theme_data
    questions = [
        {'text': 'What framework?', 'options': ['React', 'Vue', 'Svelte'], 'multi': False, 'index': 1},
        {'text': 'Which features?', 'options': ['Auth', 'DB', 'API', 'WS'], 'multi': True, 'index': 2},
        {'text': 'Project name?', 'options': None, 'multi': False, 'index': 3},
    ]
    app._log(Text('  Starting inline question flow...', style=t['accent']))
    app._log(Text('  Use arrow keys, Space to toggle, Enter to confirm', style=t['muted']))
    app._pending_questions = questions
    app._pending_answers = {}
    app._pending_notes = {}
    app._current_question_idx = 0
    app._answering_questions = True
    app._q_test_mode = True  # Don't send to agent after completing
    app._show_current_question()


@test('format-answers', 'Answer formatting')
async def test_format_answers(app):
    t = app.theme_data
    questions = [
        {'text': 'Framework?', 'options': ['React', 'Vue'], 'multi': False, 'index': 1},
        {'text': 'Features?', 'options': ['Auth', 'DB'], 'multi': True, 'index': 2},
        {'text': 'Directory?', 'options': None, 'multi': False, 'index': 3},
    ]
    answers_data = {
        'answers': {0: 'React', 1: ['Auth', 'DB'], 2: 'src/app'},
        'notes': {0: 'Latest version please', 2: 'Create if not exists'},
    }
    formatted = format_answers(questions, answers_data)
    app._log(Panel(formatted, title='Formatted Answers', border_style=t['accent'], style=f'on {t["bg_panel"]}'))
    assert '→ React' in formatted
    assert '→ Auth, DB' in formatted
    assert 'Note: Latest version' in formatted
    app._log(Text('  ✓ Answers formatted correctly with notes', style=t['success']))
    app._scroll_bottom()


# ── Plan mode tests ────────────────────────────────────────────────

@test('plan-approval', 'Plan approval inline flow')
async def test_plan_approval(app):
    t = app.theme_data
    app._last_plan_text = '# Test Plan\n\n1. Do thing A\n2. Do thing B\n\nPLAN_READY'
    app._awaiting_plan_decision = True
    app._show_plan_choices()
    app._log(Text('  Type 1 (execute), 2 (revise), 3 (cancel), or feedback text', style=t['muted']))
    app._scroll_bottom()


# ── Panel / rendering tests ────────────────────────────────────────

@test('panels', 'All panel styles with current theme')
async def test_panels(app):
    t = app.theme_data
    app._log(Text(f'\n  Theme: {t["name"]}', style=f'bold {t["accent"]}'))

    # User message
    app._log(app._themed_panel('This is a user message', title=f'[bold]user[/bold]', border_style=t['prompt_user']))

    # Bot response
    app._log(Panel(
        Markdown('**Bot response** with `code` and *emphasis* and [links](http://example.com).'),
        title='[bold]acorn[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))

    # Error
    app._log(Panel(
        Text('Connection refused — is the server running?', style=t['error']),
        title='[bold]Error[/bold]', border_style='red',
        style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))

    # Status info
    info = Table.grid(padding=(0, 2))
    info.add_row(Text('User', style=t['muted']), Text('yam', style=t['prompt_user']))
    info.add_row(Text('Theme', style=t['muted']), Text(t['name'], style=t['accent']))
    info.add_row(Text('Session', style=t['muted']), Text('cli:yam@project-abc123', style=t['fg']))
    app._log(Panel(info, title='Status', border_style=t['border'], style=f'on {t["bg_panel"]}'))

    # Plan ready
    app._log(Panel(
        Text.assemble(
            ('  1. ', f'bold {t["accent"]}'), ('▶ Execute plan\n', t['success']),
            ('  2. ', f'bold {t["accent"]}'), ('✎ Revise with feedback\n', t['fg']),
            ('  3. ', f'bold {t["accent"]}'), ('✕ Cancel\n', t['muted']),
        ),
        title='[bold]Plan Ready[/bold]', border_style=t['accent'],
        style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))

    # Queued message
    app._log(app._themed_panel(
        'fix the auth bug\n[queued — will send when current response finishes]',
        title='[bold]user[/bold] [dim](queued)[/dim]',
        border_style=t.get('muted', 'dim'),
    ))

    app._scroll_bottom()


@test('tools', 'Tool execution display')
async def test_tools(app):
    t = app.theme_data
    app._log(Text('  Simulating full tool execution sequence...', style=t['accent']))
    app._log(Text(''))

    # Thinking
    app._log(Text('  ● Thinking...', style=t['thinking']))
    await asyncio.sleep(0.3)

    # Read file
    app._log(Text('  ┌ ⚙ read_file package.json', style=t['tool_icon']))
    app._log(Text('  │ 📄 read package.json (42 lines)', style=t['read_icon']))
    app._log(Text('  └ ✓ 2ms · 1,205 chars', style=t['tool_done']))

    # Glob
    app._log(Text('  ┌ ⚙ glob **/*.ts', style=t['tool_icon']))
    app._log(Text('  └ ✓ 15ms · 47 matches', style=t['tool_done']))

    # Grep
    app._log(Text('  ┌ ⚙ grep TODO src/', style=t['tool_icon']))
    app._log(Text('  └ ✓ 8ms · 12 results', style=t['tool_done']))

    # Exec (auto-approved)
    app._log(Text('  ⚡ Auto-approved: exec: npm test', style=t['warning']))
    app._log(Text('  ┌ ⚙ exec npm test', style=t['tool_icon']))
    await asyncio.sleep(0.5)
    app._log(Text('  └ ✓ 3,420ms · 2,847 chars', style=t['tool_done']))

    # Write file
    app._log(Text('  ┌ ⚙ write_file src/new-component.tsx', style=t['tool_icon']))
    app._log(Text('  │ 📄 new src/new-component.tsx (87 lines)', style=t['read_icon']))
    app._log(Text('  └ ✓ 1ms · 2,104 chars', style=t['tool_done']))

    # Edit file
    app._log(Text('  ┌ ⚙ edit_file src/app.tsx', style=t['tool_icon']))
    app._log(Text('  │ ✏️  edit src/app.tsx', style=t['edit_icon']))
    app._log(Text('  └ ✓ 1ms · 340 chars', style=t['tool_done']))

    # Web search (server-side)
    app._log(Text('  ┌ ⚙ web_search "React 19 server components"', style=t['tool_icon']))
    app._log(Text('  └ ✓ 1,205ms · 8,432 chars', style=t['tool_done']))

    # Usage
    app._log(Text(''))
    app._log(Text('  32,104 in  4,521 out  5 iters  7 tools', style=t['usage']))
    app._scroll_bottom()


@test('themes', 'Preview all theme colors')
async def test_themes(app):
    t = app.theme_data
    app._log(Text(f'\n  Current theme: {t["name"]}', style=f'bold {t["accent"]}'))
    app._log(Text(''))

    for name in list_themes():
        theme = get_theme(name)
        row = Text()
        row.append(f'  {theme.get("icon", "?")} {name:8s}', style=f'bold {theme["accent"]}')
        row.append(f'  bg={theme["bg"]}', style=theme['muted'])

        # Color swatches
        for label, key in [('accent', 'accent'), ('success', 'success'), ('warning', 'warning'), ('error', 'error')]:
            row.append(f'  {label}=', style=theme['muted'])
            row.append('██', style=theme[key])

        # Extra info
        row.append(f'  surface={theme["bg_header"]}', style=theme['muted'])
        app._log(row)

    app._log(Text('\n  Use /theme <name> to switch', style=t['muted']))
    app._scroll_bottom()


@test('markdown', 'Markdown rendering')
async def test_markdown(app):
    t = app.theme_data
    sample = """# Heading 1

## Heading 2

### Heading 3

Regular text with **bold**, *italic*, ~~strikethrough~~, and `inline code`.

- Bullet point one
- Bullet point two
  - Nested bullet
  - Another nested
- Third point

1. Numbered one
2. Numbered two
3. Numbered three

```python
def fibonacci(n):
    if n <= 1:
        return n
    return fibonacci(n-1) + fibonacci(n-2)

# Print first 10
for i in range(10):
    print(fibonacci(i))
```

```javascript
const fetchData = async (url) => {
  const response = await fetch(url);
  return response.json();
};
```

> This is a blockquote
> spanning multiple lines

| Language   | Typing   | Speed   |
|------------|----------|---------|
| Python     | Dynamic  | Medium  |
| TypeScript | Static   | Fast    |
| Rust       | Static   | Fastest |

---

A horizontal rule above, and a [link](https://example.com) here.
"""
    app._log(Panel(
        Markdown(sample),
        title='[bold]Markdown Rendering Test[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))
    app._scroll_bottom()


@test('streaming', 'Simulated streaming response')
async def test_streaming(app):
    t = app.theme_data

    chunks = [
        'Here is a ', 'simulated ', 'streaming ', 'response.\n\n',
        'Each chunk ', 'arrives ', 'with a ', 'small delay. ',
        'The panel ', 'updates ', 'in real-time.\n\n',
        '**Bold** ', 'and `code` ', 'work too.\n\n',
        '```python\n', 'print("hello")\n', '```\n',
    ]

    # Simulate streaming by writing chunks with delay
    app._log(Text(f'  acorn ▸ ', style=f'bold {t["accent"]}'))
    full_text = ''
    for chunk in chunks:
        full_text += chunk
        await asyncio.sleep(0.08)

    # Final render as panel (same as real chat:done)
    app._log(Panel(
        Markdown(full_text),
        title='[bold]acorn[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))
    app._log(Text('  1,204 in  89 out  1 iters', style=t['usage']))
    app._scroll_bottom()


# ── Environment tests ──────────────────────────────────────────────

@test('env', 'Environment audit')
async def test_env(app):
    t = app.theme_data
    env = gather_environment()
    proj_type = detect_project_type(app.cwd)

    app._log(Panel(
        Text(env, style=t['fg']),
        title='[bold]Environment Audit[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))
    app._log(Text(f'  Project type: {proj_type}', style=t['accent']))
    app._scroll_bottom()


@test('bg', 'Background process management')
async def test_bg(app):
    t = app.theme_data

    app._log(Text('  Launching test background process...', style=t['accent']))
    bp = await app.process_manager.launch('echo "hello from bg" && sleep 1 && echo "done"', app.cwd)
    app._log(Text(f'  ⚡ Background #{bp.id}: started', style=t['success']))

    await asyncio.sleep(1.5)

    app._log(Text(f'  Process #{bp.id} status: {"running" if bp.running else f"done (exit {bp.exit_code})"}', style=t['fg']))
    output = '\n'.join(bp.output)
    app._log(Text(f'  Output: {output}', style=t['fg']))

    # Check state
    assert not bp.running, 'Process should have finished'
    assert 'hello from bg' in output
    assert 'done' in output
    app._log(Text('  ✓ Background process ran and captured output', style=t['success']))

    # Cleanup
    app.process_manager.remove(bp.id)
    app._log(Text(f'  ✓ Removed #{bp.id}', style=t['muted']))
    app._scroll_bottom()


@test('path-sandbox', 'File operation path sandboxing')
async def test_path_sandbox(app):
    t = app.theme_data
    from acorn.tools.file_ops import read_file, write_file, edit_file

    # Should fail — path outside cwd
    result = read_file({'path': '/etc/passwd'}, app.cwd)
    assert 'error' in result and 'outside' in result['error'].lower(), f'Expected sandbox error, got: {result}'
    app._log(Text('  ✓ /etc/passwd blocked (outside cwd)', style=t['success']))

    result = read_file({'path': '../../etc/shadow'}, app.cwd)
    assert 'error' in result, f'Expected sandbox error for traversal'
    app._log(Text('  ✓ ../../etc/shadow blocked (path traversal)', style=t['success']))

    # Should work — file inside cwd (if exists, otherwise not-found is fine)
    result = read_file({'path': 'nonexistent.txt'}, app.cwd)
    assert 'outside' not in result.get('error', '').lower()
    app._log(Text('  ✓ nonexistent.txt in cwd → allowed (file not found, not sandboxed)', style=t['success']))

    app._log(Text(f'\n  All sandbox tests passed', style=f'bold {t["success"]}'))
    app._scroll_bottom()


@test('connection', 'Connection state')
async def test_connection(app):
    t = app.theme_data

    try:
        ws_ok = app.conn.ws is not None and not app.conn.ws.closed
    except AttributeError:
        ws_ok = app.conn.ws is not None
    app._log(Text(f'  WebSocket connected: {ws_ok}', style=t['success'] if ws_ok else t['error']))
    app._log(Text(f'  Host: {app.conn.host}:{app.conn.port}', style=t['fg']))
    app._log(Text(f'  Session: {app.session_id}', style=t['fg']))
    app._log(Text(f'  User: {app.user}', style=t['fg']))
    app._log(Text(f'  Plan mode: {app.plan_mode}', style=t['fg']))
    app._log(Text(f'  Generating: {app.generating}', style=t['fg']))
    app._log(Text(f'  Messages sent: {app._message_count}', style=t['fg']))
    app._log(Text(f'  Queued message: {bool(app._queued_message)}', style=t['fg']))
    app._log(Text(f'  Background processes: {app.process_manager.running_count}', style=t['fg']))

    # Test auth endpoint
    from acorn.connection import Connection
    test_conn = Connection(app.conn.host, app.conn.port)
    try:
        # This should fail with bad key
        await test_conn.authenticate('test', 'bad_key')
        app._log(Text('  ✗ Auth should have rejected bad key', style=t['error']))
    except Exception as e:
        app._log(Text(f'  ✓ Bad key rejected: {e}', style=t['success']))

    app._scroll_bottom()


@test('header-footer', 'Header and footer rendering')
async def test_header_footer(app):
    t = app.theme_data

    # Test header collapse
    was_collapsed = app._header_collapsed
    app._header_collapsed = False
    app._update_header()
    app._log(Text('  Header set to full logo mode', style=t['muted']))
    await asyncio.sleep(0.5)

    app._header_collapsed = True
    app._current_activity = 'read_file src/test.py'
    app._update_header()
    app._log(Text('  Header collapsed with activity', style=t['muted']))
    await asyncio.sleep(0.5)

    app._current_activity = ''
    app._update_header()
    app._log(Text('  Header collapsed, idle', style=t['muted']))
    await asyncio.sleep(0.5)

    # Test footer updates
    app._update_footer()
    app._log(Text('  Footer updated (current state)', style=t['muted']))

    # Restore
    app._header_collapsed = was_collapsed
    app._update_header()
    app._log(Text('  ✓ Header/footer rendering OK', style=t['success']))
    app._scroll_bottom()


# ── Command entry point ────────────────────────────────────────────

@command('/test')
async def cmd_test(args, **ctx):
    app = ctx.get('app')
    if not app:
        return

    name = args.strip()

    if not name or name == 'list':
        t = app.theme_data
        app._log(Text('\n  Available tests:', style=f'bold {t["accent"]}'))
        for tname, tinfo in sorted(TESTS.items()):
            app._log(Text(f'    /test {tname:18s} {tinfo["desc"]}', style=t['fg']))
        app._log(Text(f'    /test all                Run all tests', style=t['fg']))
        app._log(Text(''))
        app._scroll_bottom()
        return

    if name == 'all':
        passed = 0
        failed = 0
        for tname, tinfo in sorted(TESTS.items()):
            t = app.theme_data
            app._log(Rule(f'Test: {tname}', style=t['separator']))
            try:
                await tinfo['fn'](app)
                passed += 1
            except Exception as e:
                app._log(Text(f'  ✗ FAILED: {e}', style=t['error']))
                failed += 1
            app._log(Text(''))

        t = app.theme_data
        app._log(Rule(style=t['separator']))
        summary = f'  {passed} passed'
        if failed:
            summary += f', {failed} failed'
        style = t['success'] if not failed else t['error']
        app._log(Text(summary, style=f'bold {style}'))
        app._scroll_bottom()
        return

    if name in TESTS:
        t = app.theme_data
        app._log(Rule(f'Test: {name}', style=t['separator']))
        try:
            await TESTS[name]['fn'](app)
        except Exception as e:
            app._log(Text(f'  ✗ FAILED: {e}', style=t['error']))
        app._scroll_bottom()
    else:
        app._log(Text(f'  Unknown test: {name}. Use /test list', style='red'))
        app._scroll_bottom()
