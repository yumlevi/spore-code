"""Background process manager — run and monitor long-lived processes."""

import asyncio
import time
import os
from collections import deque


class BackgroundProcess:
    """A single background process with captured output."""

    def __init__(self, pid, command, proc):
        self.id = pid
        self.command = command
        self.proc = proc
        self.output = deque(maxlen=500)  # last 500 lines
        self.started = time.time()
        self.ended = None
        self.exit_code = None
        self._task = None

    @property
    def running(self):
        return self.proc.returncode is None

    @property
    def elapsed(self):
        end = self.ended or time.time()
        secs = int(end - self.started)
        if secs < 60:
            return f'{secs}s'
        elif secs < 3600:
            return f'{secs // 60}m {secs % 60}s'
        return f'{secs // 3600}h {(secs % 3600) // 60}m'

    def kill(self):
        try:
            self.proc.kill()
        except Exception:
            pass


class ProcessManager:
    """Manages background processes launched by the agent or user."""

    def __init__(self):
        self._processes = {}
        self._next_id = 1

    async def launch(self, command: str, cwd: str) -> BackgroundProcess:
        """Launch a command in the background and start capturing output."""
        proc = await asyncio.create_subprocess_shell(
            command, cwd=cwd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
        )
        pid = self._next_id
        self._next_id += 1
        bp = BackgroundProcess(pid, command, proc)
        self._processes[pid] = bp
        bp._task = asyncio.create_task(self._read_output(bp))
        return bp

    async def _read_output(self, bp: BackgroundProcess):
        """Read stdout lines and store in the process buffer."""
        try:
            while True:
                line = await bp.proc.stdout.readline()
                if not line:
                    break
                bp.output.append(line.decode('utf-8', errors='replace').rstrip('\n'))
        except Exception:
            pass
        finally:
            try:
                await bp.proc.wait()
            except Exception:
                pass
            bp.exit_code = bp.proc.returncode
            bp.ended = time.time()

    def list_all(self):
        """Return all processes (running + finished)."""
        return list(self._processes.values())

    def get(self, pid: int):
        return self._processes.get(pid)

    def kill(self, pid: int) -> bool:
        bp = self._processes.get(pid)
        if bp and bp.running:
            bp.kill()
            return True
        return False

    def remove(self, pid: int):
        bp = self._processes.get(pid)
        if bp and not bp.running:
            del self._processes[pid]
            return True
        return False

    @property
    def running_count(self):
        return sum(1 for bp in self._processes.values() if bp.running)

    def kill_all(self):
        """Kill all running background processes. Called on exit."""
        for bp in self._processes.values():
            if bp.running:
                try:
                    bp.kill()
                except Exception:
                    pass
