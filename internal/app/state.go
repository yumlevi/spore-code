package app

// AppState mirrors acorn/state.py:AppState. The Go model uses a flag +
// modal kind combo already, so this is a minimal parity layer — mainly
// useful for logging and for the observer response to perm:query.
type AppState int

const (
	StateIdle AppState = iota
	StateStreaming
	StateToolPending
	StatePermissionPrompt
	StateQuestions
	StatePlanReview
	StatePlanFeedback
	StateGenerating
	StateDisconnected
)

func (s AppState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStreaming:
		return "streaming"
	case StateToolPending:
		return "tool-pending"
	case StatePermissionPrompt:
		return "permission-prompt"
	case StateQuestions:
		return "questions"
	case StatePlanReview:
		return "plan-review"
	case StatePlanFeedback:
		return "plan-feedback"
	case StateGenerating:
		return "generating"
	case StateDisconnected:
		return "disconnected"
	}
	return "unknown"
}

// currentState derives the state from model flags + modal.
func (m *Model) currentState() AppState {
	if !m.connected {
		return StateDisconnected
	}
	switch m.modal {
	case modalPermission:
		return StatePermissionPrompt
	case modalQuestion:
		return StateQuestions
	case modalPlan:
		if m.planApproval != nil && m.planApproval.noting {
			return StatePlanFeedback
		}
		return StatePlanReview
	}
	if m.currentStreamIdx >= 0 {
		return StateStreaming
	}
	if m.generating {
		return StateGenerating
	}
	return StateIdle
}
