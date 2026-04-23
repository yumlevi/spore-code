package app

import (
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/yumlevi/acorn-cli/go/internal/proto"
)

// lightweight style helpers shared by modals (defined once so each modal
// file doesn't re-declare accent/muted).
func accentBold(t Theme) lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Accent).Bold(true) }
func mutedStyleT(t Theme) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Muted).Faint(true) }
func bodyStyleT(t Theme) lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Fg) }

// question represents a single question in a QUESTIONS: block or an
// ask_user tool call. Open-ended questions have Options == nil.
type question struct {
	Text    string
	Options []string
	Multi   bool
}

type questionModal struct {
	// Source of the question set:
	//   source == "prose"  — parsed from QUESTIONS: block; answers flow back
	//                        as a free-form chat message.
	//   source == "ask_user" — structured tool call; answer flows back as a
	//                          ws message with the matching qid.
	source string
	qid    string

	questions []question
	answers   []string // per-question chosen option (single-select) or joined (multi)
	idx       int
	selected  int
	checked   map[int]bool // multi-select
}

func (m *Model) openQuestionModal(qs []question) {
	m.modal = modalQuestion
	m.question = &questionModal{
		source:    "prose",
		questions: qs,
		answers:   make([]string, len(qs)),
		checked:   map[int]bool{},
	}
	// Mirror Python's questions.py:start_questions — tell the companion
	// app so mobile observers can render the same sheet.
	items := make([]map[string]any, 0, len(qs))
	for i, q := range qs {
		item := map[string]any{"text": q.Text, "multi": q.Multi, "index": i + 1}
		if q.Options != nil {
			item["options"] = q.Options
		}
		items = append(items, item)
	}
	m.Broadcast("state:questions", map[string]any{"questions": items})
}

func (m *Model) openStructuredQuestion(f proto.AskUser) {
	labels := make([]string, 0, len(f.Options))
	for _, o := range f.Options {
		labels = append(labels, o.Label)
	}
	m.modal = modalQuestion
	m.question = &questionModal{
		source: "ask_user",
		qid:    f.QID,
		questions: []question{{
			Text:    f.Question,
			Options: labels,
		}},
		answers: make([]string, 1),
		checked: map[int]bool{},
	}
}

// parseQuestionsBlock parses the QUESTIONS: marker format used by acorn's
// prose-based question flow (mirrored from acorn/questions.py so CLI behaviour
// matches the Python implementation).
func parseQuestionsBlock(text string) []question {
	if text == "" {
		return nil
	}
	// Split on the QUESTIONS: marker — only parse if it's explicit.
	parts := regexp.MustCompile(`(?mi)(?:^|\n)\s*QUESTIONS?\s*:\s*\n`).Split(text, -1)
	if len(parts) < 2 {
		return nil
	}
	body := parts[len(parts)-1]

	// Take the first segment that has numbered items.
	blank := regexp.MustCompile(`\n\s*\n`)
	var seg string
	for _, s := range blank.Split(body, -1) {
		if regexp.MustCompile(`(?m)^\s*\d+\.`).MatchString(s) {
			seg = s
			break
		}
	}
	if seg == "" {
		return nil
	}

	itemRe := regexp.MustCompile(`(?m)^\s*\d+\.\s+(.+?)$`)
	multiRe := regexp.MustCompile(`\{([^}]+ / [^}]+)\}`)
	singleRe := regexp.MustCompile(`\[([^\]]+ / [^\]]+)\]`)

	var qs []question
	for _, m := range itemRe.FindAllStringSubmatch(seg, -1) {
		raw := strings.TrimSpace(m[1])
		var q question
		if mm := multiRe.FindStringSubmatchIndex(raw); mm != nil {
			opts := splitOptions(raw[mm[2]:mm[3]])
			q.Text = strings.TrimRight(strings.TrimSpace(raw[:mm[0]]), "?") + "?"
			q.Options = opts
			q.Multi = true
		} else if mm := singleRe.FindStringSubmatchIndex(raw); mm != nil {
			opts := splitOptions(raw[mm[2]:mm[3]])
			q.Text = strings.TrimRight(strings.TrimSpace(raw[:mm[0]]), "?") + "?"
			q.Options = opts
		} else {
			q.Text = strings.TrimRight(raw, "?") + "?"
		}
		qs = append(qs, q)
	}
	if len(qs) == 0 {
		return nil
	}
	return qs
}

// splitOptions splits on " / " at top level (not inside parens) — matches
// _split_options in questions.py.
func splitOptions(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c == '(' {
			depth++
			cur.WriteByte(c)
			i++
		} else if c == ')' {
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
			i++
		} else if depth == 0 && i+3 <= len(s) && s[i:i+3] == " / " {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			i += 3
		} else {
			cur.WriteByte(c)
			i++
		}
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	// Drop empties.
	filtered := out[:0]
	for _, v := range out {
		if v != "" {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// view renders the modal as a centred card.
func (qm *questionModal) view(w, h int) string {
	if qm.idx >= len(qm.questions) {
		return ""
	}
	q := qm.questions[qm.idx]
	lines := []string{
		accentStyle.Bold(true).Render(
			"Question "+itoa(qm.idx+1)+"/"+itoa(len(qm.questions))+": ") + q.Text,
		"",
	}
	if q.Options != nil {
		for i, opt := range q.Options {
			cursor := " "
			if i == qm.selected {
				cursor = "▸"
			}
			marker := " "
			if q.Multi {
				if qm.checked[i] {
					marker = "◉"
				} else {
					marker = "○"
				}
			}
			line := " " + cursor + " " + marker + " " + opt
			if i == qm.selected {
				line = accentStyle.Bold(true).Render(line)
			}
			lines = append(lines, line)
		}
		if q.Multi {
			lines = append(lines, "", mutedStyle.Render(" ↑↓ move · space toggle · enter submit · esc cancel"))
		} else {
			lines = append(lines, "", mutedStyle.Render(" ↑↓ select · enter confirm · esc cancel"))
		}
	} else {
		lines = append(lines, mutedStyle.Render(" (open-ended — type in the input then press Enter)"))
	}

	inner := strings.Join(lines, "\n")
	boxW := w - 10
	if boxW < 40 {
		boxW = w - 4
	}
	box := borderStyle.Copy().
		BorderForeground(lipgloss.Color("#8b6cf7")).
		Width(boxW).
		Padding(1, 2).
		Render(inner)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(lipgloss.Color("#0e1017")))
}

// updateModal handles keystrokes while a modal is open.
func (m *Model) updateModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Non-key messages still need to flow. The loop's frame reader must keep
	// being re-armed while a modal is open, otherwise the server's delta/
	// status frames would silently stop being read.
	switch v := msg.(type) {
	case wsFrameMsg:
		cmd := m.handleFrame(v.frame)
		return m, tea.Batch(cmd, m.recvCmd())
	case toolHandledMsg:
		return m, m.toolCmd()
	case tea.WindowSizeMsg:
		return m.handleResize(v.Width, v.Height)
	case sizePollMsg:
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			if w != m.width || h != m.height {
				mm, c := m.handleResize(w, h)
				return mm, tea.Batch(c, sizePollCmd())
			}
		}
		return m, sizePollCmd()
	case connOpenMsg, connErrorMsg, connClosedMsg:
		// surface as regular state changes even under modal
	}

	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	// Ctrl+C inside a modal: dismiss the modal first (and deny any
	// pending permission prompt so the blocked tool goroutine unblocks).
	// THEN route through the normal handleCtrlC double-tap logic so it
	// behaves the same as outside modals.
	if km.String() == "ctrl+c" {
		if m.modal == modalPermission && m.perms != nil {
			m.perms.resolvePerm(false, false)
		}
		m.modal = modalNone
		m.question = nil
		m.planApproval = nil
		m.permission = nil
		return m.handleCtrlC()
	}
	if km.String() == "ctrl+d" {
		if m.modal == modalPermission && m.perms != nil {
			m.perms.resolvePerm(false, false)
		}
		return m, m.shutdownCmd()
	}

	switch m.modal {
	case modalQuestion:
		return m.updateQuestionModal(km)
	case modalPlan:
		return m.updatePlanModal(km)
	case modalPermission:
		return m.updatePermModal(km)
	}
	return m, nil
}

func (m *Model) updateQuestionModal(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	qm := m.question
	if qm == nil {
		m.modal = modalNone
		return m, nil
	}
	if qm.idx >= len(qm.questions) {
		return m.finishQuestions()
	}
	q := qm.questions[qm.idx]

	switch km.String() {
	case "esc":
		m.modal = modalNone
		m.question = nil
		m.pushChat("system", "Questions cancelled.")
		return m, nil
	case "up":
		if q.Options != nil {
			qm.selected = (qm.selected - 1 + len(q.Options)) % len(q.Options)
		}
		return m, nil
	case "down":
		if q.Options != nil {
			qm.selected = (qm.selected + 1) % len(q.Options)
		}
		return m, nil
	case " ", "space":
		if q.Options != nil && q.Multi {
			qm.checked[qm.selected] = !qm.checked[qm.selected]
		}
		return m, nil
	case "enter":
		if q.Options == nil {
			// Open-ended — treat m.input contents as the answer.
			qm.answers[qm.idx] = strings.TrimSpace(m.input.Value())
			m.input.Reset()
		} else if q.Multi {
			var picks []string
			for i := range q.Options {
				if qm.checked[i] {
					picks = append(picks, q.Options[i])
				}
			}
			qm.answers[qm.idx] = strings.Join(picks, ", ")
		} else {
			qm.answers[qm.idx] = q.Options[qm.selected]
		}
		qm.idx++
		qm.selected = 0
		qm.checked = map[int]bool{}
		if qm.idx >= len(qm.questions) {
			return m.finishQuestions()
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) finishQuestions() (tea.Model, tea.Cmd) {
	qm := m.question
	m.modal = modalNone
	m.question = nil
	if qm == nil {
		return m, nil
	}

	// Format + send.
	var answerBody string
	if qm.source == "ask_user" {
		// Structured ask_user: send WS answer.
		if len(qm.answers) > 0 {
			_ = m.client.Send(map[string]any{
				"type":   "ask_user_answer",
				"qid":    qm.qid,
				"answer": qm.answers[0],
			})
			m.pushChat("system", "Answered: "+qm.answers[0])
		}
		return m, nil
	}

	// Prose path: format all answers into a follow-up chat message.
	var lines []string
	lines = append(lines, "Here are my answers to your questions:")
	for i, q := range qm.questions {
		ans := qm.answers[i]
		if ans == "" {
			ans = "(skipped)"
		}
		lines = append(lines, "")
		lines = append(lines, itoa(i+1)+". "+q.Text)
		lines = append(lines, "   → "+ans)
	}
	answerBody = strings.Join(lines, "\n")
	m.pushChat("user", answerBody)
	m.generating = true
	m.status = "waiting…"
	// Dismiss mobile question sheet.
	m.Broadcast("interactive:resolved", map[string]any{"kind": "questions"})
	return m, m.sendChat(answerBody, answerBody)
}

// itoa avoids importing strconv for small ints in hot view paths.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
