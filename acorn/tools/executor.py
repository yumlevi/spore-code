"""Tool dispatch — routes tool:request to local handlers or signals server fallback."""

from acorn.tools import file_ops, shell, search, serve

# Tools executed locally on the user's machine
LOCAL_TOOLS = {'exec', 'read_file', 'write_file', 'edit_file', 'glob', 'grep', 'web_fetch', 'web_serve'}

# Tools that must stay server-side (operate on Anima internals)
SERVER_TOOLS = {
    'graph_query', 'graph_update', 'graph_delete', 'query_about',
    'message_send', 'message_react', 'message_edit', 'message_read',
    'delegate_task', 'task_status', 'task_cancel', 'task_update',
    'save_tool', 'skill_lookup', 'skill_update',
    'session_status', 'sessions_list', 'env_manage',
    'notify_user', 'web_search',
    'anima_list', 'anima_message', 'anima_graph', 'anima_manage',
    'browser', 'startup_tasks', 'data_poller',
    'remote_exec', 'remote_read_file', 'remote_write_file', 'ssh_tunnel',
    'list_custom_tools',
}


class ToolExecutor:
    def __init__(self, permissions, renderer, cwd: str, process_manager=None):
        self.permissions = permissions
        self.renderer = renderer
        self.cwd = cwd
        self.process_manager = process_manager
        self.delegation_mode = 'default'  # synced from ContextManager
        # Log dir for exec output files — .acorn/logs/ in the project
        import os
        self._log_dir = os.path.join(cwd, '.acorn', 'logs')

    async def execute(self, name: str, input: dict) -> "dict | None":
        """Execute a tool locally. Returns None to signal server-side fallback."""

        # Enforce delegation restrictions — intercept delegate_task before server handles it
        if name == 'delegate_task':
            mode = self.delegation_mode
            if mode == 'off':
                return {'error': 'Delegation is disabled. Do this task yourself inline using the available tools (read_file, write_file, exec, etc.).'}
            if mode == 'research':
                task_desc = (input.get('task', '') + ' ' + input.get('context', '')).lower()
                has_write = any(w in task_desc for w in ['write', 'create file', 'edit file', 'generate code', 'build', 'implement', 'scaffold'])
                if has_write:
                    return {'error': 'Delegation mode is "research" — you can only delegate web research, not file writes. Write the code yourself using write_file/edit_file so it lands on the user\'s machine.'}
            if mode == 'default':
                task_desc = (input.get('task', '') + ' ' + input.get('context', '')).lower()
                has_orchestration = any(w in task_desc for w in ['build the', 'implement the', 'create the project', 'scaffold', 'set up the'])
                if has_orchestration:
                    return {'error': 'Delegation mode is "default" — do not delegate main task orchestration. Stay interactive with the user. You may delegate parallel research or parallel file writes only.'}
            # Allow — let server handle
            return None

        if name in SERVER_TOOLS or name not in LOCAL_TOOLS:
            return None

        if not self.permissions.is_auto_approved(name, input):
            approved = await self.permissions.prompt(name, input)
            if not approved:
                return {'error': 'Denied by user'}

        if name == 'read_file':
            return file_ops.read_file(input, self.cwd)
        elif name == 'write_file':
            return file_ops.write_file(input, self.cwd)
        elif name == 'edit_file':
            return file_ops.edit_file(input, self.cwd)
        elif name == 'exec':
            return await shell.execute(input, self.cwd, process_manager=self.process_manager, log_dir=self._log_dir, on_output=getattr(self, '_on_output', None))
        elif name == 'glob':
            return search.glob_search(input, self.cwd)
        elif name == 'grep':
            return search.grep_search(input, self.cwd)
        elif name == 'web_serve':
            return self._handle_web_serve(input)
        elif name == 'web_fetch':
            return None  # delegate to server
        else:
            return None

    def _handle_web_serve(self, input: dict) -> dict:
        """Handle web_serve locally — spin up an HTTP server on the user's machine."""
        action = input.get('action', 'start')
        if action == 'start':
            directory = input.get('dir', input.get('directory', self.cwd))
            port = input.get('port', 0)
            return serve.start_server(directory, port)
        elif action == 'stop':
            port = input.get('port', 0)
            return serve.stop_server(port)
        elif action == 'status':
            servers = serve.list_servers()
            return {'servers': servers, 'count': len(servers)}
        else:
            return serve.start_server(self.cwd)
