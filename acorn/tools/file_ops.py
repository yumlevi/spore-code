"""Local file operations — read, write, edit."""

import os


def _resolve(filepath: str, cwd: str) -> str:
    if os.path.isabs(filepath):
        resolved = os.path.normpath(filepath)
    else:
        resolved = os.path.normpath(os.path.join(cwd, filepath))
    # Enforce: must be within cwd
    if not resolved.startswith(os.path.normpath(cwd) + os.sep) and resolved != os.path.normpath(cwd):
        raise PermissionError(f'Path {resolved} is outside the working directory {cwd}')
    return resolved


def read_file(input: dict, cwd: str) -> dict:
    try:
        filepath = _resolve(input.get('path', ''), cwd)
    except PermissionError as e:
        return {'error': str(e)}
    if not os.path.exists(filepath):
        return {'error': f'File not found: {filepath}'}
    try:
        offset = input.get('offset', 0)
        limit = input.get('limit', 2000)
        with open(filepath, 'r', errors='replace') as f:
            lines = f.readlines()
        selected = lines[offset:offset + limit]
        content = ''.join(f'{i + offset + 1}\t{line}' for i, line in enumerate(selected))
        return {'content': content, 'totalLines': len(lines)}
    except Exception as e:
        return {'error': str(e)}


def write_file(input: dict, cwd: str) -> dict:
    try:
        filepath = _resolve(input.get('path', ''), cwd)
    except PermissionError as e:
        return {'error': str(e)}
    content = input.get('content', '')
    try:
        os.makedirs(os.path.dirname(filepath) or '.', exist_ok=True)
        with open(filepath, 'w') as f:
            f.write(content)
        return {'ok': True, 'path': filepath, 'lines': content.count('\n') + 1}
    except Exception as e:
        return {'error': str(e)}


def edit_file(input: dict, cwd: str) -> dict:
    try:
        filepath = _resolve(input.get('path', ''), cwd)
    except PermissionError as e:
        return {'error': str(e)}
    if not os.path.exists(filepath):
        return {'error': f'File not found: {filepath}'}
    try:
        with open(filepath, 'r') as f:
            text = f.read()
        old = input.get('old_string', input.get('old_text', ''))
        new = input.get('new_string', input.get('new_text', ''))
        if old not in text:
            return {'error': f'old_string not found in {filepath}'}
        count = text.count(old)
        replace_all = input.get('replace_all', False)
        if count > 1 and not replace_all:
            return {'error': f'old_string found {count} times — not unique. Provide more context or use replace_all.'}
        updated = text.replace(old, new) if replace_all else text.replace(old, new, 1)
        with open(filepath, 'w') as f:
            f.write(updated)
        return {'ok': True, 'path': filepath, 'replacements': count if replace_all else 1}
    except Exception as e:
        return {'error': str(e)}
