"""Unit tests for shell execution."""

import asyncio
import pytest
from acorn.tools.shell import check_path_safety, get_command_binary, execute, SAFE_COMMANDS


def test_safe_commands_populated():
    assert 'git' in SAFE_COMMANDS
    assert 'node' in SAFE_COMMANDS
    assert 'python3' in SAFE_COMMANDS
    assert len(SAFE_COMMANDS) > 50


def test_command_binary():
    assert get_command_binary('git status') == 'git'
    assert get_command_binary('sudo npm install') == 'npm'
    assert get_command_binary('/usr/bin/python3 app.py') == 'python3'


def test_path_safety_blocks_ssh():
    assert check_path_safety('cat ~/.ssh/id_rsa') != ''


def test_path_safety_blocks_expanded():
    import os
    home = os.path.expanduser('~')
    assert check_path_safety(f'cat {home}/.ssh/id_rsa') != ''


def test_path_safety_allows_normal():
    assert check_path_safety('cat /etc/hosts') == ''
    assert check_path_safety('ls src/') == ''


@pytest.mark.asyncio
async def test_execute():
    result = await execute({'command': 'echo hello_test'}, '/tmp')
    assert result.get('exitCode') == 0
    assert 'hello_test' in result.get('output', '')


@pytest.mark.asyncio
async def test_dangerous_blocked():
    result = await execute({'command': 'rm -rf /'}, '/tmp')
    assert 'error' in result
