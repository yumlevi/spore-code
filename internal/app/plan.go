package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/spore-code/internal/proto"
)

type planModal struct {
	text     string // the plan's prose body
	selected int    // 0=execute, 1=revise, 2=cancel
	feedback string // when in revise-feedback entry mode
	noting   bool
}

func (m *Model) openPlanModal(text string) {
	m.setWorkflowPhase(workflowPlanApproval, "")
	m.modal = modalPlan
	m.planApproval = &planModal{text: text}
	// Relay plan text to companion observers (mobile shows same modal).
	preview := truncateCells(text, 2000)
	m.Broadcast("plan:show-approval", map[string]any{"text": preview})
}

func (pm *planModal) view(w, h int, t Theme) string {
	// Inline render: the plan text itself is already in the chat
	// history above (the assistant's reply is what triggered this
	// modal), so we drop the embedded preview and just show the
	// action UI in the input slot. Matches the Python acorn UX —
	// the user reads the plan in the chat scrollback, picks an
	// action below.
	if h < 3 {
		h = 3
	}
	boxW := w - 2
	if boxW < 8 {
		boxW = w
	}
	if boxW < 1 {
		boxW = 1
	}
	innerW := boxW - 4
	if innerW < 8 {
		innerW = boxW
	}
	if innerW < 1 {
		innerW = 1
	}
	innerLimit := h - 2
	if innerLimit < 1 {
		innerLimit = 1
	}

	var lines []string
	lines = append(lines, truncateCells(t.accent(true).Render("Plan Ready")+t.muted().Render("  — review the plan above and choose:"), innerW))
	if pm.noting {
		maxFeedbackLines := innerLimit - 4
		if maxFeedbackLines < 1 {
			maxFeedbackLines = 1
		}
		feedbackLines := strings.Split(wrapForPanel(pm.feedback+"▌", innerW-2), "\n")
		feedbackLines = clipLinesTail(feedbackLines, maxFeedbackLines)
		lines = append(lines,
			"Feedback for the agent (enter submits, esc cancels):",
			borderStyle.Copy().
				BorderForeground(t.Accent2).
				Width(innerW).
				Padding(0, 1).
				Render(strings.Join(feedbackLines, "\n")),
		)
	} else {
		choices := []struct{ label, desc string }{
			{"▶ Execute plan", "save + switch to execute mode"},
			{"✎ Revise with feedback", "keep planning, agent will revise"},
			{"✕ Cancel", "discard the plan"},
		}
		for i, c := range choices {
			cursor := "  "
			label := c.label
			if i == pm.selected {
				cursor = "▸ "
				label = t.accent(true).Render(c.label)
			}
			lines = append(lines, truncateCells(cursor+label+t.muted().Render("  "+c.desc), innerW))
		}
		lines = append(lines, t.muted().Render(" ↑↓ select · enter confirm · esc cancel"))
	}

	lines = clipLinesHead(lines, innerLimit)
	return borderStyle.Copy().
		BorderForeground(t.Accent).
		Width(boxW).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func (m *Model) updatePlanModal(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	pm := m.planApproval
	if pm == nil {
		m.modal = modalNone
		return m, nil
	}

	if pm.noting {
		switch km.Type {
		case tea.KeyEsc:
			m.inputBurst = nil
			m.inputBurstScheduled = false
			m.inputBurstNormalize = false
			pm.noting = false
			pm.feedback = ""
			return m, nil
		case tea.KeyEnter:
			m.flushPendingInputText()
			return m.planReviseWithFeedback(strings.TrimSpace(pm.feedback))
		case tea.KeyBackspace:
			m.flushPendingInputText()
			if len(pm.feedback) > 0 {
				runes := []rune(pm.feedback)
				pm.feedback = string(runes[:len(runes)-1])
			}
			return m, nil
		}
		if cmd, consumed := m.handleTextInputKey(km); consumed {
			return m, cmd
		}
		m.flushPendingInputText()
		return m, nil
	}

	switch km.String() {
	case "esc":
		m.modal = modalNone
		m.planApproval = nil
		m.setWorkflowPhase(workflowIdle, "")
		m.Broadcast("plan:decided", map[string]any{"action": "cancel"})
		m.pushChat("system", "Plan dismissed.")
		return m, nil
	case "up":
		pm.selected = (pm.selected - 1 + 3) % 3
		return m, nil
	case "down":
		pm.selected = (pm.selected + 1) % 3
		return m, nil
	case "enter":
		switch pm.selected {
		case 0:
			return m.planExecute(pm.text)
		case 1:
			m.flushPendingInputText()
			pm.noting = true
			pm.feedback = ""
			return m, nil
		case 2:
			m.modal = modalNone
			m.planApproval = nil
			m.setWorkflowPhase(workflowIdle, "")
			m.Broadcast("plan:decided", map[string]any{"action": "cancel"})
			m.pushChat("system", "Plan discarded.")
			return m, nil
		}
	}
	return m, nil
}

// planExecute saves the plan, flips to execute mode, and sends PLAN_EXECUTE.
func (m *Model) planExecute(text string) (tea.Model, tea.Cmd) {
	if path := savePlan(m.cwd, text); path != "" {
		m.pushChat("system", "Plan saved to "+path)
	} else {
		m.pushChat("system", "Plan save FAILED — check permissions on .spore-code/plans/")
	}
	m.planMode = false
	m.modal = modalNone
	m.planApproval = nil
	m.setWorkflowPhase(workflowExecuting, "")
	m.Broadcast("plan:decided", map[string]any{"action": "execute"})
	m.Broadcast("plan:set-mode", map[string]any{"enabled": false})
	m.pushChat("system", "Mode → execute")
	m.pushChat("system", "▶ Executing plan…")
	m.startActiveTurn("waiting…")
	// Use sendChat (not the old raw sendChatMessage) so projectContext
	// flows on this turn with mode='execute'. Without it the system
	// prompt loses the Project Context section AND the plan-mode block,
	// AND the server can't tell what mode we're in for the turn that's
	// about to actually do the writes. With it, the agent sees a fresh
	// system prompt where the plan-mode rules are gone and the regular
	// tool set (write_file, edit_file, exec) is unrestricted.
	// Batch with spinnerTickCmd so the activity spinner kicks back on for
	// this turn — without it, the spinner ticker stopped at the previous
	// chat:done and there's no UI signal that the agent is actually
	// working through the plan.
	return m, tea.Batch(m.sendChatWithMode(PlanExecuteMsg, "execute"), spinnerTickCmd())
}

func (m *Model) planReviseWithFeedback(fb string) (tea.Model, tea.Cmd) {
	m.modal = modalNone
	m.planApproval = nil
	if fb == "" {
		m.pushChat("system", "Plan revise cancelled (empty feedback).")
		return m, nil
	}
	m.pushChat("user", "(feedback) "+fb)
	m.startActiveTurn("waiting…")
	m.setWorkflowPhase(workflowInterview, "revision")
	m.Broadcast("plan:decided", map[string]any{"action": "revise", "feedback": fb})
	// Stay in plan mode — projectContext.mode='plan' so the system
	// prompt keeps emitting the plan-mode block on the revise turn.
	return m, tea.Batch(m.sendChatWithMode("[PLAN FEEDBACK: Revise the plan. Stay in plan mode.]\n\n"+fb, "plan"), spinnerTickCmd())
}

// sendChatWithMode is the plan-flow equivalent of update.go's enter
// path — builds the structured projectContext with an explicit mode
// override (execute / plan) and sends through the same sendChat
// pipeline so capability detection + glue fallback both still work.
func (m *Model) sendChatWithMode(content, mode string) tea.Cmd {
	var pc *proto.ProjectContext
	if m.serverCaps.ProjectContext {
		built := BuildProjectContextWithScope(m.cwd, mode, m.scope)
		pc = &built
	} else if mode == "plan" {
		// Legacy fallback: glue the prefix on like the old client did
		// when we don't have the structured channel.
		content = PlanPrefix + content
	}
	return m.sendChat(content, content, pc)
}

// savePlan mirrors acorn/cli.py:_save_plan — writes the plan to
// {cwd}/.spore-code/plans/plan-<ts>.md. Returns empty string on failure.
func savePlan(cwd, text string) string {
	dir := filepath.Join(cwd, ".spore-code", "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "[plan-save] mkdir:", err)
		return ""
	}
	ts := time.Now().Format("20060102-150405")
	name := "plan-" + ts + ".md"
	full := filepath.Join(dir, name)
	clean := strings.TrimSpace(strings.ReplaceAll(text, "PLAN_READY", ""))
	body := "# Plan — " + ts + "\n\n" + clean + "\n"
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "[plan-save] write:", err)
		return ""
	}
	return full
}
