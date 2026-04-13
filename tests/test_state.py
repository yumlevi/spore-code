"""Unit tests for state machine."""

from acorn.state import StateMachine, AppState


def test_initial_state():
    sm = StateMachine()
    assert sm.state == AppState.IDLE


def test_valid_transition():
    sm = StateMachine()
    assert sm.transition(AppState.GENERATING)
    assert sm.state == AppState.GENERATING


def test_invalid_transition():
    sm = StateMachine()
    assert not sm.transition(AppState.PERMISSION_PROMPT)  # can't go directly to permission from idle
    assert sm.state == AppState.IDLE


def test_transition_chain():
    sm = StateMachine()
    assert sm.transition(AppState.GENERATING)
    assert sm.transition(AppState.STREAMING)
    assert sm.transition(AppState.TOOL_PENDING)
    assert sm.transition(AppState.IDLE)


def test_is_busy():
    sm = StateMachine()
    assert not sm.is_busy
    sm.transition(AppState.GENERATING)
    assert sm.is_busy


def test_accepts_chat():
    sm = StateMachine()
    assert sm.accepts_chat
    sm.transition(AppState.GENERATING)
    assert not sm.accepts_chat


def test_can_stop():
    sm = StateMachine()
    assert not sm.can_stop
    sm.transition(AppState.GENERATING)
    assert sm.can_stop


def test_previous():
    sm = StateMachine()
    sm.transition(AppState.GENERATING)
    assert sm.previous == AppState.IDLE


def test_listener():
    sm = StateMachine()
    events = []
    sm.on_change(lambda old, new: events.append((old, new)))
    sm.transition(AppState.GENERATING)
    assert events == [(AppState.IDLE, AppState.GENERATING)]


def test_same_state():
    sm = StateMachine()
    sm.transition(AppState.GENERATING)
    assert sm.transition(AppState.GENERATING)  # no-op, returns True


def test_questions_from_generating():
    sm = StateMachine()
    sm.transition(AppState.GENERATING)
    assert sm.transition(AppState.QUESTIONS)


def test_disconnected():
    sm = StateMachine()
    sm.transition(AppState.GENERATING)
    assert sm.transition(AppState.DISCONNECTED)
    assert sm.transition(AppState.IDLE)
