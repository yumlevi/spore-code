"""Unit tests for config validation."""

import io
import sys
from acorn.config import _validate_config


def test_valid_config():
    """Valid config should produce no warnings."""
    old_stderr = sys.stderr
    sys.stderr = io.StringIO()
    _validate_config({'connection': {'host': 'x', 'port': 80}, 'display': {'theme': 'dark'}})
    output = sys.stderr.getvalue()
    sys.stderr = old_stderr
    assert output == ''


def test_typo_section():
    old_stderr = sys.stderr
    sys.stderr = io.StringIO()
    _validate_config({'conection': {'host': 'x'}})
    output = sys.stderr.getvalue()
    sys.stderr = old_stderr
    assert 'connection' in output  # did you mean


def test_typo_key():
    old_stderr = sys.stderr
    sys.stderr = io.StringIO()
    _validate_config({'connection': {'hst': 'x'}})
    output = sys.stderr.getvalue()
    sys.stderr = old_stderr
    assert 'host' in output  # did you mean
