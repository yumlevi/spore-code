"""State machine for the Acorn TUI — replaces boolean flag soup."""

from enum import Enum, auto


class AppState(Enum):
    IDLE = auto()              # Waiting for user input
    STREAMING = auto()         # Receiving chat:delta events
    TOOL_PENDING = auto()      # Tool executing (local or server)
    PERMISSION_PROMPT = auto() # Showing tool approval selector
    QUESTIONS = auto()         # Interactive question selector active
    PLAN_REVIEW = auto()       # Plan approval selector active
    PLAN_FEEDBACK = auto()     # Typing feedback for plan revision
    GENERATING = auto()        # Agent is processing (waiting for first event)
    DISCONNECTED = auto()      # WebSocket lost

# Valid state transitions
TRANSITIONS = {
    AppState.IDLE: {
        AppState.GENERATING, AppState.STREAMING, AppState.QUESTIONS,
        AppState.PLAN_REVIEW, AppState.PLAN_FEEDBACK, AppState.DISCONNECTED,
    },
    AppState.GENERATING: {
        AppState.STREAMING, AppState.TOOL_PENDING, AppState.IDLE,
        AppState.QUESTIONS, AppState.PLAN_REVIEW, AppState.DISCONNECTED,
    },
    AppState.STREAMING: {
        AppState.TOOL_PENDING, AppState.IDLE, AppState.GENERATING,
        AppState.QUESTIONS, AppState.PLAN_REVIEW, AppState.DISCONNECTED,
    },
    AppState.TOOL_PENDING: {
        AppState.PERMISSION_PROMPT, AppState.STREAMING,
        AppState.GENERATING, AppState.IDLE, AppState.DISCONNECTED,
    },
    AppState.PERMISSION_PROMPT: {
        AppState.TOOL_PENDING, AppState.STREAMING,
        AppState.GENERATING, AppState.IDLE,
    },
    AppState.QUESTIONS: {
        AppState.IDLE, AppState.GENERATING,
    },
    AppState.PLAN_REVIEW: {
        AppState.IDLE, AppState.PLAN_FEEDBACK, AppState.GENERATING,
    },
    AppState.PLAN_FEEDBACK: {
        AppState.PLAN_REVIEW, AppState.GENERATING, AppState.IDLE,
    },
    AppState.DISCONNECTED: {
        AppState.IDLE, AppState.GENERATING,
    },
}


class StateMachine:
    """Tracks the TUI's current state with validated transitions."""

    def __init__(self):
        self.state = AppState.IDLE
        self._previous = AppState.IDLE
        self._listeners = []

    def on_change(self, callback):
        """Register a listener for state changes."""
        self._listeners.append(callback)

    def transition(self, new_state: AppState, force: bool = False) -> bool:
        """Transition to new state. Returns False if invalid (unless forced)."""
        if new_state == self.state:
            return True
        if not force:
            valid = TRANSITIONS.get(self.state, set())
            if new_state not in valid:
                return False
        self._previous = self.state
        self.state = new_state
        for cb in self._listeners:
            try:
                cb(self._previous, new_state)
            except Exception:
                pass
        return True

    def force(self, new_state: AppState):
        """Force a transition regardless of validity."""
        self.transition(new_state, force=True)

    @property
    def previous(self) -> AppState:
        return self._previous

    @property
    def is_busy(self) -> bool:
        """True when the agent is doing work."""
        return self.state in (
            AppState.STREAMING, AppState.GENERATING,
            AppState.TOOL_PENDING, AppState.PERMISSION_PROMPT,
        )

    @property
    def is_generating(self) -> bool:
        """True when we're in any agent-processing state."""
        return self.state in (
            AppState.GENERATING, AppState.STREAMING, AppState.TOOL_PENDING,
        )

    @property
    def accepts_chat(self) -> bool:
        """True when the user can type a new message."""
        return self.state == AppState.IDLE

    @property
    def accepts_input(self) -> bool:
        """True when ANY input is valid (chat, questions, plan feedback)."""
        return self.state in (
            AppState.IDLE, AppState.QUESTIONS,
            AppState.PLAN_REVIEW, AppState.PLAN_FEEDBACK,
        )

    @property
    def can_stop(self) -> bool:
        """True when Ctrl+C should stop generation."""
        return self.is_busy

    def __repr__(self):
        return f'StateMachine({self.state.name})'
