"""Test commands — /test <name> to exercise UI features locally without hitting the agent."""

import asyncio
from acorn.commands.registry import command
from acorn.questions import parse_questions, QuestionScreen, format_answers
from acorn.themes import get_theme, list_themes
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


@test('questions', 'Interactive question flow')
async def test_questions(app):
    t = app.theme_data
    questions = [
        {'text': 'What framework are you using?', 'options': ['React', 'Vue', 'Svelte', 'Angular'], 'index': 1},
        {'text': 'Do you want TypeScript?', 'options': ['Yes', 'No'], 'index': 2},
        {'text': 'What is the target directory?', 'options': None, 'index': 3},
        {'text': 'Which package manager?', 'options': ['npm', 'yarn', 'pnpm', 'bun'], 'index': 4},
    ]
    app._log(Text(f'  Testing interactive questions ({len(questions)} questions)', style=t['accent']))
    app._scroll_bottom()
    app.app_questions = questions

    def on_done(answers):
        if answers is None:
            app._log(Text('  Test cancelled', style=t['muted']))
        else:
            formatted = format_answers(questions, answers)
            app._log(app._themed_panel(formatted, title='[bold]Answers[/bold]', border_style=t['success']))
        app._scroll_bottom()

    app.push_screen(QuestionScreen(questions, t), callback=on_done)


@test('question-parse', 'Parse questions from sample agent text')
async def test_question_parse(app):
    t = app.theme_data
    sample = """I have a few questions before I can plan this:

QUESTIONS:
1. What database are you using? [PostgreSQL / MySQL / SQLite / MongoDB]
2. Do you need authentication? [Yes / No]
3. What's the expected number of users?
4. Which cloud provider? [AWS / GCP / Azure / Self-hosted]
"""
    parsed = parse_questions(sample)
    app._log(Text(f'  Parsed {len(parsed)} questions from sample text:', style=t['accent']))
    for q in parsed:
        opts = f' [{" / ".join(q["options"])}]' if q['options'] else ' (open-ended)'
        app._log(Text(f'    {q["index"]}. {q["text"]}{opts}', style=t['fg']))
    app._scroll_bottom()


@test('panels', 'All panel styles with current theme')
async def test_panels(app):
    t = app.theme_data

    app._log(Text(f'\n  Theme: {t["name"]}', style=f'bold {t["accent"]}'))

    # User panel
    app._log(app._themed_panel('This is what a user message looks like', title=f'[bold]user[/bold]', border_style=t['prompt_user']))

    # Bot panel
    app._log(Panel(
        Markdown('This is what a **bot response** looks like with `code` and *emphasis*.'),
        title='[bold]acorn[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))

    # Error panel
    app._log(Panel(
        Text('Something went wrong', style=t['error']),
        title='[bold]Error[/bold]', border_style='red',
        style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))

    # Info panel
    info = Table.grid(padding=(0, 2))
    info.add_row(Text('Key', style=t['muted']), Text('Value', style=t['fg']))
    info.add_row(Text('Theme', style=t['muted']), Text(t['name'], style=t['accent']))
    app._log(Panel(info, title='Status', border_style=t['border'], style=f'on {t["bg_panel"]}'))

    app._scroll_bottom()


@test('tools', 'Tool execution display')
async def test_tools(app):
    t = app.theme_data

    app._log(Text('  Simulating tool execution...', style=t['accent']))

    # Thinking
    app._log(Text('  ● Thinking...', style=t['thinking']))

    # Tool start/done
    app._log(Text('  ┌ ⚙ exec ls -la /src', style=t['tool_icon']))
    app._log(Text('  └ ✓ 45ms · 1,234 chars', style=t['tool_done']))

    # File read
    app._log(Text('  ┌ ⚙ read_file src/app.py', style=t['tool_icon']))
    app._log(Text('  │ 📄 read src/app.py (142 lines)', style=t['read_icon']))
    app._log(Text('  └ ✓ 2ms · 4,521 chars', style=t['tool_done']))

    # File edit
    app._log(Text('  ┌ ⚙ edit_file src/config.py', style=t['tool_icon']))
    app._log(Text('  │ ✏️  edit src/config.py', style=t['edit_icon']))
    app._log(Text('  └ ✓ 3ms · 128 chars', style=t['tool_done']))

    # Auto-approved
    app._log(Text('  ⚡ Auto-approved: exec: npm test', style=t['warning']))

    # Usage stats
    app._log(Text('  7,104 in  73 out  2 iters  3 tools', style=t['usage']))

    app._scroll_bottom()


@test('themes', 'Preview all theme colors')
async def test_themes(app):
    t = app.theme_data

    for name in list_themes():
        theme = get_theme(name)
        row = Text()
        row.append(f'  {theme.get("icon", "?")} {name:8s}', style=f'bold {theme["accent"]}')
        row.append(f'  bg={theme["bg"]}', style=theme['muted'])
        row.append(f'  accent=', style=theme['muted'])
        row.append('████', style=theme['accent'])
        row.append('  success=', style=theme['muted'])
        row.append('████', style=theme['success'])
        row.append('  warning=', style=theme['muted'])
        row.append('████', style=theme['warning'])
        app._log(row)
    app._scroll_bottom()


@test('markdown', 'Markdown rendering')
async def test_markdown(app):
    t = app.theme_data
    sample = """# Heading 1

## Heading 2

Regular text with **bold**, *italic*, and `inline code`.

- Bullet point one
- Bullet point two
  - Nested bullet

```python
def hello():
    print("Hello from Acorn!")
```

> This is a blockquote

| Column A | Column B |
|----------|----------|
| Value 1  | Value 2  |
"""
    app._log(Panel(
        Markdown(sample),
        title='[bold]Markdown Test[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))
    app._scroll_bottom()


@test('streaming', 'Simulated streaming response')
async def test_streaming(app):
    t = app.theme_data
    from acorn.protocol import chat_message

    chunks = [
        'Here is a ', 'simulated ', 'streaming ', 'response. ',
        'Each chunk ', 'arrives ', 'with a ', 'small delay.\n\n',
        '**Bold text** ', 'and `code` ', 'work too. ',
        'The panel ', 'updates in ', 'real-time.'
    ]

    app._stream_buffer = ''
    app._response_text = []

    try:
        stream = app.query_one('#stream-area')
    except Exception:
        app._log(Text('  Stream area not found', style='red'))
        return

    for chunk in chunks:
        app._stream_buffer += chunk
        app._response_text.append(chunk)
        try:
            stream.update(Panel(
                Markdown(app._stream_buffer),
                title='[bold]acorn[/bold]', title_align='left',
                border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
            ))
        except Exception:
            stream.update(app._stream_buffer)
        app._scroll_bottom()
        await asyncio.sleep(0.15)

    # Finalize
    stream.update('')
    final = ''.join(app._response_text)
    app._log(Panel(
        Markdown(final),
        title='[bold]acorn[/bold]', title_align='left',
        border_style=t['accent'], style=f'on {t["bg_panel"]}', padding=(0, 1),
    ))
    app._log(Text('  150 in  42 out', style=t['usage']))
    app._stream_buffer = ''
    app._response_text = []
    app._scroll_bottom()


@command('/test')
async def cmd_test(args, **ctx):
    app = ctx.get('app')
    if not app:
        # Fallback: use renderer for non-TUI mode
        ctx['renderer'].show_error('/test only works in TUI mode')
        return

    name = args.strip()

    if not name or name == 'list':
        t = app.theme_data
        app._log(Text('\n  Available tests:', style=f'bold {t["accent"]}'))
        for tname, tinfo in TESTS.items():
            app._log(Text(f'    /test {tname:16s} {tinfo["desc"]}', style=t['fg']))
        app._log(Text(f'    /test all              Run all tests', style=t['fg']))
        app._log(Text(''))
        app._scroll_bottom()
        return

    if name == 'all':
        for tname, tinfo in TESTS.items():
            t = app.theme_data
            app._log(Rule(f'Test: {tname}', style=t['separator']))
            await tinfo['fn'](app)
            app._log(Text(''))
        app._scroll_bottom()
        return

    if name in TESTS:
        t = app.theme_data
        app._log(Rule(f'Test: {name}', style=t['separator']))
        await TESTS[name]['fn'](app)
        app._scroll_bottom()
    else:
        app._log(Text(f'  Unknown test: {name}. Use /test list', style='red'))
        app._scroll_bottom()
