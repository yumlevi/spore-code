"""Constants — plan prompts, logo, slash command definitions."""

PLAN_PREFIX = (
    '[MODE: Plan only. You are in planning mode. Follow these phases in order:\n\n'

    'PHASE 1 — ENVIRONMENT AUDIT:\n'
    'The context above includes the local environment (OS, installed tools, runtimes). '
    'Review what is available. If the task requires tools/runtimes not installed, note them. '
    'Use exec to check versions or configs if needed (e.g. `node --version`, `cat package.json`).\n\n'

    'PHASE 2 — CODEBASE SCAN:\n'
    'Use read_file, glob, and grep to understand the existing codebase structure, patterns, '
    'conventions, config files, and dependencies. Identify what exists and what needs to change.\n\n'

    'PHASE 3 — RESEARCH:\n'
    'Identify topics you need more context on — frameworks, APIs, libraries, best practices. '
    'Use web_search and web_fetch to research them. For example:\n'
    '  - "Next.js 14 app router best practices 2024"\n'
    '  - "Tailwind CSS v4 setup guide"\n'
    '  - API docs for libraries you plan to use\n'
    'Do actual searches — don\'t rely on stale training knowledge for fast-moving tools.\n\n'

    'PHASE 4 — CLARIFY:\n'
    'If you have questions for the user, you MUST use this EXACT format with the QUESTIONS: marker on its own line. '
    'Do NOT embed questions in the plan text. Ask them BEFORE presenting the plan.\n\n'
    'QUESTIONS:\n'
    '1. Single-select question? [Option A / Option B / Option C]\n'
    '2. Multi-select question? {Option A / Option B / Option C / Option D}\n'
    '3. Open-ended question?\n\n'
    'The QUESTIONS: marker is REQUIRED — without it the client cannot detect your questions. '
    '[brackets] = single-select, {braces} = multi-select checkboxes, no brackets = open text input. '
    'If you have questions, output ONLY the QUESTIONS: block and STOP — do NOT include PLAN_READY in the same response. '
    'Wait for answers before presenting the plan.\n\n'

    'PHASE 5 — PLAN:\n'
    'Only after questions are answered (or if you have none), present a detailed plan with:\n'
    '  - Prerequisites (what needs to be installed/configured first)\n'
    '  - Step-by-step changes with file paths\n'
    '  - New files to create vs existing files to modify\n'
    '  - Dependencies to install\n'
    '  - Any commands to run\n'
    '  - How to verify it works\n\n'

    'RULES:\n'
    '- Do NOT make changes (no write_file, edit_file).\n'
    '- Do NOT run destructive or modifying commands.\n'
    '- You MAY use: read_file, glob, grep, web_search, web_fetch, exec (read-only commands only like ls, cat, which, --version).\n'
    '- Do NOT put questions and PLAN_READY in the same response — ask first, then plan after answers.\n'
    '- End your plan with "PLAN_READY" on its own line.]\n\n'
)

PLAN_EXECUTE_MSG = (
    '[The user has approved the plan above. Switch to execute mode and implement it now. '
    'Proceed step by step, executing all the changes you outlined.]'
)

LOGO_FULL = r"""
     ██████╗  ██████╗ ██████╗ ██████╗ ███╗   ██╗
    ██╔══██╗██╔════╝██╔═══██╗██╔══██╗████╗  ██║
    ███████║██║     ██║   ██║██████╔╝██╔██╗ ██║
    ██╔══██║██║     ██║   ██║██╔══██╗██║╚██╗██║
    ██║  ██║╚██████╗╚██████╔╝██║  ██║██║ ╚████║
    ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝"""

LOGO_MINI = ' 🌰 acorn'

SLASH_COMMANDS = [
    ('/help', 'Show available commands'),
    ('/quit', 'Exit Acorn'),
    ('/clear', 'Clear session history'),
    ('/stop', 'Stop current generation'),
    ('/plan', 'Toggle plan mode'),
    ('/status', 'Connection info'),
    ('/theme', 'Switch theme (dark, light, oak, forest, oled, ...)'),
    ('/mode', 'Tool approval mode (auto, ask, locked)'),
    ('/mode auto', 'Auto-approve non-dangerous tools'),
    ('/mode ask', 'Prompt for every tool'),
    ('/mode locked', 'Deny all writes/exec'),
    ('/mode rules', 'Show session allow rules'),
    ('/approve-all', 'Shortcut for /mode auto'),
    ('/approve-all-dangerous', 'YOLO — approve everything, no exceptions'),
    ('/mode yolo', 'Approve everything including dangerous commands'),
    ('/test', 'Run UI tests'),
    ('/test all', 'Run all tests'),
    ('/bg', 'List background processes'),
    ('/bg run', 'Run command in background'),
    ('/bg kill', 'Kill a background process'),
    ('/sessions', 'List saved sessions for this project'),
    ('/update', 'Pull & install latest version'),
    ('/update check', 'Check for updates without installing'),
    ('/delegate', 'Task delegation settings'),
    ('/delegate default', 'Research+bg ok, orchestration local'),
    ('/delegate off', 'No delegation at all'),
    ('/delegate research', 'Only parallel web research'),
    ('/delegate code', 'Research + parallel file writes'),
    ('/delegate all', 'Unrestricted delegation'),
]
