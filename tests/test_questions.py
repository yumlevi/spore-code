"""Unit tests for question parser."""

from acorn.questions import parse_questions, format_answers


def test_standard_block():
    text = "QUESTIONS:\n1. DB? [PG / MySQL]\n2. Features? {A / B / C}\n3. Count?"
    parsed = parse_questions(text)
    assert len(parsed) == 3
    assert parsed[0]['options'] == ['PG', 'MySQL']
    assert parsed[0]['multi'] is False
    assert parsed[1]['multi'] is True
    assert parsed[2]['options'] is None


def test_no_marker():
    assert parse_questions("1. Step one\n2. Step two") == []


def test_parentheses_not_options():
    text = "QUESTIONS:\n1. Python (Flask/FastAPI)?\n2. PM? [npm / yarn]"
    parsed = parse_questions(text)
    assert parsed[0]['options'] is None
    assert parsed[1]['options'] == ['npm', 'yarn']


def test_blank_line_after_marker():
    text = "QUESTIONS:\n\n1. First? [A / B]\n2. Second?"
    assert len(parse_questions(text)) == 2


def test_plan_ready_with_questions():
    text = "QUESTIONS:\n\n1. Version? [A / B]\n\nPLAN_READY"
    assert len(parse_questions(text)) == 1


def test_format_answers():
    questions = [{'text': 'Q1?', 'options': ['A'], 'multi': False, 'index': 1}]
    data = {'answers': {0: 'A'}, 'notes': {0: 'note'}}
    formatted = format_answers(questions, data)
    assert '→ A' in formatted
    assert 'Note: note' in formatted


def test_format_multi_select():
    questions = [{'text': 'Q?', 'options': ['X', 'Y'], 'multi': True, 'index': 1}]
    data = {'answers': {0: ['X', 'Y']}, 'notes': {}}
    formatted = format_answers(questions, data)
    assert '→ X, Y' in formatted
