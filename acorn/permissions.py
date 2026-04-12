"""Permission system for tool execution — auto-approve reads, prompt for writes."""

import asyncio
from rich.prompt import Confirm

AUTO_APPROVE = {'read_file', 'glob', 'grep'}


class Permissions:
    def __init__(self, renderer=None):
        self.approve_all = False
        self.renderer = renderer

    def is_auto_approved(self, tool_name: str, input: dict) -> bool:
        if self.approve_all:
            return True
        return tool_name in AUTO_APPROVE

    async def prompt(self, tool_name: str, input: dict) -> bool:
        if self.approve_all:
            return True
        summary = self._summarize(tool_name, input)
        # Stop Live display so the prompt is visible
        if self.renderer:
            self.renderer._stop_live()
        loop = asyncio.get_event_loop()
        result = await loop.run_in_executor(
            None, lambda: Confirm.ask(f'  Allow [bold]{tool_name}[/bold]: {summary}?', default=True)
        )
        # Restart Live display
        if self.renderer:
            self.renderer._ensure_live()
        return result

    def _summarize(self, name: str, input: dict) -> str:
        if name == 'exec':
            return input.get('command', '')[:120]
        if name in ('write_file', 'edit_file', 'read_file'):
            return input.get('path', '')
        if name == 'web_fetch':
            return input.get('url', '')[:100]
        return str(input)[:80]
