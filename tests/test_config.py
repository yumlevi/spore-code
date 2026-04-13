"""Unit tests for config validation."""

from acorn.config import _validate_config


def test_valid_config(capsys):
    _validate_config({'connection': {'host': 'x', 'port': 80}, 'display': {'theme': 'dark'}})
    assert capsys.readouterr().err == ''


def test_typo_section(capsys):
    _validate_config({'conection': {'host': 'x'}})
    assert 'connection' in capsys.readouterr().err


def test_typo_key(capsys):
    _validate_config({'connection': {'hst': 'x'}})
    assert 'host' in capsys.readouterr().err
