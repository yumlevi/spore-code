"""WebSocket client — auth, connection, message routing, reconnect."""

import asyncio
import json
import urllib.request
import urllib.error
from urllib.parse import urlparse
import websockets


class AuthError(Exception):
    pass


def _build_base_url(host: str, port: int) -> str:
    if '://' in host:
        return host.rstrip('/')
    return f'http://{host}:{port}'


def _build_ws_url(base_url: str) -> str:
    if base_url.startswith('https://'):
        return 'wss://' + base_url[8:]
    elif base_url.startswith('http://'):
        return 'ws://' + base_url[7:]
    return 'ws://' + base_url


class Connection:
    def __init__(self, host: str, port: int):
        self.host = host
        self.port = port
        self.base_url = _build_base_url(host, port)
        self.ws_base = _build_ws_url(self.base_url)
        self.ws = None
        self.token = None
        self.tool_executor = None
        self._handlers = {}
        self._receive_task = None
        self._heartbeat_task = None
        self._outbox = []        # messages queued during disconnect
        self._reconnecting = False
        self._username = None
        self._key = None
        self._slog = None        # session logger
        self._on_disconnect = None  # callback
        self._on_reconnect = None   # callback
        self._on_tool_output = None  # callback for output log (after completion)
        self._on_tool_line = None   # callback for live output lines (during exec)
        self.connected = False

    async def authenticate(self, username: str, key: str) -> str:
        self._username = username
        self._key = key
        url = f'{self.base_url}/api/acorn/auth'
        payload = json.dumps({'username': username, 'key': key}).encode()
        req = urllib.request.Request(url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                data = json.loads(resp.read())
                self.token = data['token']
                return self.token
        except urllib.error.HTTPError as e:
            body = e.read().decode()
            try:
                data = json.loads(body)
                raise AuthError(data.get('error', f'HTTP {e.code}'))
            except (json.JSONDecodeError, AuthError):
                raise
            raise AuthError(f'HTTP {e.code}: {body[:200]}')

    async def connect(self, token: str):
        url = f'{self.ws_base}/ws?token={token}'
        self.ws = await websockets.connect(url, ping_interval=20, ping_timeout=10, max_size=10 * 1024 * 1024)
        self.connected = True
        self._receive_task = asyncio.create_task(self._receive_loop())
        self._heartbeat_task = asyncio.create_task(self._heartbeat_loop())

    async def close(self):
        self.connected = False
        if self._heartbeat_task:
            self._heartbeat_task.cancel()
        if self._receive_task:
            self._receive_task.cancel()
        if self.ws:
            try:
                await self.ws.close()
            except Exception:
                pass

    def on(self, msg_type: str, handler):
        self._handlers[msg_type] = handler

    async def send(self, data: str):
        """Send a message. If disconnected, queue it for reconnect."""
        if self.ws and self.connected:
            try:
                await self.ws.send(data)
                return
            except (websockets.ConnectionClosed, Exception):
                pass
        # Queue for later
        self._outbox.append(data)
        if self._slog:
            self._slog.debug('ws', f'queued message ({len(self._outbox)} in outbox)')
        if not self._reconnecting:
            asyncio.create_task(self._reconnect())

    async def _receive_loop(self):
        try:
            async for raw in self.ws:
                try:
                    msg = json.loads(raw)
                except json.JSONDecodeError:
                    continue
                msg_type = msg.get('type', '')

                if msg_type == 'tool:request' and self.tool_executor:
                    # Acknowledge immediately so server knows we're alive
                    tool_id = msg.get('id', '')
                    if tool_id:
                        try:
                            await self.send(json.dumps({'type': 'tool:ack', 'id': tool_id}))
                        except Exception:
                            pass
                    asyncio.create_task(self._handle_tool_request(msg))
                    continue

                handler = self._handlers.get(msg_type)
                if handler:
                    try:
                        await handler(msg)
                    except Exception:
                        pass
        except websockets.ConnectionClosed:
            if self._slog:
                self._slog.warn('ws', 'connection closed')
            self.connected = False
            if self._on_disconnect:
                try:
                    self._on_disconnect()
                except Exception:
                    pass
            if not self._reconnecting:
                asyncio.create_task(self._reconnect())
        except asyncio.CancelledError:
            pass

    async def _heartbeat_loop(self):
        """Periodic ping to detect dead connections early."""
        while self.connected:
            try:
                await asyncio.sleep(15)
                if self.ws and self.connected:
                    pong = await self.ws.ping()
                    await asyncio.wait_for(pong, timeout=5)
            except (asyncio.TimeoutError, websockets.ConnectionClosed, Exception):
                if self._slog:
                    self._slog.warn('ws', 'heartbeat failed — reconnecting')
                self.connected = False
                if not self._reconnecting:
                    asyncio.create_task(self._reconnect())
                break
            except asyncio.CancelledError:
                break

    async def _reconnect(self):
        """Reconnect with exponential backoff. Flushes outbox on success."""
        if self._reconnecting:
            return
        self._reconnecting = True
        backoff = [1, 2, 4, 8, 15, 30, 30, 30]

        if self._slog:
            self._slog.info('ws', f'reconnecting... ({len(self._outbox)} queued)')

        # Tear down old connection fully before opening a new one —
        # prevents ghost WebSocket that echoes messages back to us
        await self._teardown_old_ws()

        for attempt, delay in enumerate(backoff):
            try:
                # Re-authenticate (token may have expired)
                if self._username and self._key:
                    token = await asyncio.get_event_loop().run_in_executor(
                        None, lambda: self._sync_auth(self._username, self._key)
                    )
                    self.token = token

                # Reconnect WebSocket
                url = f'{self.ws_base}/ws?token={self.token}'
                self.ws = await websockets.connect(url, ping_interval=20, ping_timeout=10, max_size=10 * 1024 * 1024)
                self.connected = True

                # Restart receive + heartbeat loops
                self._receive_task = asyncio.create_task(self._receive_loop())
                self._heartbeat_task = asyncio.create_task(self._heartbeat_loop())

                # Flush outbox
                flushed = 0
                while self._outbox:
                    msg = self._outbox.pop(0)
                    try:
                        await self.ws.send(msg)
                        flushed += 1
                    except Exception:
                        self._outbox.insert(0, msg)
                        break

                if self._slog:
                    self._slog.info('ws', f'reconnected (attempt {attempt+1}, flushed {flushed} messages)')

                self._reconnecting = False
                if self._on_reconnect:
                    try:
                        self._on_reconnect()
                    except Exception:
                        pass
                return

            except Exception as e:
                if self._slog:
                    self._slog.debug('ws', f'reconnect attempt {attempt+1} failed: {e}')
                await asyncio.sleep(delay)

        if self._slog:
            self._slog.error('ws', 'all reconnect attempts failed')
        self._reconnecting = False

    async def _teardown_old_ws(self):
        """Close the old WebSocket and cancel its background tasks so the server
        fires 'close' and removes the stale client from session tracking."""
        if self._heartbeat_task:
            self._heartbeat_task.cancel()
            self._heartbeat_task = None
        if self._receive_task:
            self._receive_task.cancel()
            self._receive_task = None
        if self.ws:
            try:
                await self.ws.close()
            except Exception:
                pass
            self.ws = None

    def _sync_auth(self, username, key):
        """Synchronous auth for use in executor."""
        url = f'{self.base_url}/api/acorn/auth'
        payload = json.dumps({'username': username, 'key': key}).encode()
        req = urllib.request.Request(url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read())
            return data['token']

    async def _handle_tool_request(self, msg: dict):
        tool_id = msg.get('id', '')
        tool_name = msg.get('name', '')
        tool_input = msg.get('input', {})
        import time as _time
        start = _time.time()
        if self._slog:
            self._slog.tool_request(tool_name, tool_input)
        try:
            # Set streaming callback on executor (avoids kwarg mismatch across versions)
            self.tool_executor._on_output = self._on_tool_line if tool_name == 'exec' else None
            result = await self.tool_executor.execute(tool_name, tool_input)
            ms = int((_time.time() - start) * 1000)
            local = result is not None
            if self._slog:
                self._slog.tool_result(tool_name, result, local=local, duration_ms=ms)
            # Also persist to session writer
            if hasattr(self, '_session_writer') and self._session_writer:
                self._session_writer.write_tool(tool_name, tool_input, result, local, ms)
            # Notify output log callback if set
            if self._on_tool_output:
                try:
                    self._on_tool_output(tool_name, tool_input, result, ms)
                except Exception:
                    pass
            await self.send(json.dumps({'type': 'tool:result', 'id': tool_id, 'result': result}))
        except Exception as e:
            if self._slog:
                self._slog.exception('tool', e)
            await self.send(json.dumps({'type': 'tool:result', 'id': tool_id, 'result': {'error': str(e)}}))
