package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// codeViewEntry tracks a single file view/diff/thinking event in the
// chronological activity feed shown by the right-column panel.
type codeViewEntry struct {
	Path     string
	Content  string // full body (truncated) for code-view entries
	OldText  string
	NewText  string
	Text     string // short label like "203 lines, 4124 bytes" or "+12/-3 lines"
	Preview  string // a few-line peek shown under the header in the panel
	IsDiff   bool
	IsNew    bool
	Thinking bool // true when this is an accumulated agent-reasoning entry
	Tool     string
	ExecCmd  string
	ExecOut  []string
	When     time.Time

	// Memo for the rendered preview lines. Keyed by inner column width
	// so a resize invalidates. Stops appendThinking from re-running
	// wordWrap on every single chat:thinking token.
	cachedW       int
	cachedThinkV  int // bumped when Content changes so we know to re-wrap
	cachedPreview []string
	dirtyVer      int
}

// pushCodeView records a read_file / write_file hit.
func (m *Model) pushCodeView(path, content string, isNew bool) {
	lineCount := strings.Count(content, "\n") + 1
	e := codeViewEntry{
		Path:    path,
		Content: truncateStr(content, 4000),
		Preview: previewLines(content, 4, 120),
		Text:    fmt.Sprintf("%d lines, %d bytes", lineCount, len(content)),
		IsNew:   isNew,
		When:    time.Now(),
	}
	m.appendActivity(e)
}

// pushCodeDiff records an edit_file hit.
func (m *Model) pushCodeDiff(path, oldT, newT string) {
	added := strings.Count(newT, "\n")
	removed := strings.Count(oldT, "\n")
	m.appendActivity(codeViewEntry{
		Path:    path,
		OldText: truncateStr(oldT, 2000),
		NewText: truncateStr(newT, 2000),
		Preview: diffPreview(oldT, newT, 4, 120),
		Text:    fmt.Sprintf("+%d / -%d lines", added, removed),
		IsDiff:  true,
		When:    time.Now(),
	})
}

// appendThinking accumulates a chat:thinking text chunk. Successive
// chunks within the same thinking block are merged into one entry so
// the panel doesn't churn one row per token. Bumps dirtyVer so the
// per-entry preview cache re-renders on next View().
//
// IMPORTANT: don't drop whitespace-only chunks. Models stream tokens
// individually — `word` and ` ` and `next` arrive as three separate
// deltas. Throwing away the lone-space chunk glues `wordnext`
// together and produces the run-on text we saw in early builds.
func (m *Model) appendThinking(text string) {
	if text == "" {
		return
	}
	if n := len(m.codeViews); n > 0 && m.codeViews[n-1].Thinking {
		last := &m.codeViews[n-1]
		last.Content = truncateStr(last.Content+text, 4000)
		last.Text = fmt.Sprintf("%d chars", len(last.Content))
		last.When = time.Now()
		last.dirtyVer++
		return
	}
	m.appendActivity(codeViewEntry{
		Thinking: true,
		Content:  truncateStr(text, 4000),
		Text:     fmt.Sprintf("%d chars", len(text)),
		When:     time.Now(),
	})
}

// appendToolExec records a server-side tool execution (status frame
// arrives with name + detail) — gives the user a quick sense of what
// the agent is doing besides reads/writes.
func (m *Model) appendToolExec(tool, detail string) {
	m.appendActivity(codeViewEntry{
		Tool:    tool,
		ExecCmd: detail,
		Preview: trimTo(detail, 240),
		Text:    "tool",
		When:    time.Now(),
	})
}

func (m *Model) appendActivity(e codeViewEntry) {
	m.codeViews = append(m.codeViews, e)
	if len(m.codeViews) > 100 {
		m.codeViews = m.codeViews[len(m.codeViews)-100:]
	}
}

// previewLines returns up to maxLines from s, each clipped to maxW.
// Skips empty leading lines so previews don't waste rows on blank
// padding from the start of a file.
func previewLines(s string, maxLines, maxW int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, maxLines)
	for _, ln := range lines {
		if len(out) == 0 && strings.TrimSpace(ln) == "" {
			continue
		}
		if len(ln) > maxW {
			ln = ln[:maxW-1] + "…"
		}
		out = append(out, ln)
		if len(out) >= maxLines {
			break
		}
	}
	return strings.Join(out, "\n")
}

// diffPreview shows a few lines from the new text — for an edit it's
// usually the just-written content. Stripped to the first non-blank
// run so the panel surfaces the actually-interesting hunk.
func diffPreview(oldT, newT string, maxLines, maxW int) string {
	if newT == "" {
		return previewLines(oldT, maxLines, maxW)
	}
	return previewLines(newT, maxLines, maxW)
}

// renderCodePanel returns the compact right-column activity panel:
// thinking bursts + file reads/writes/edits, newest at the bottom,
// each with a small content preview. Height is hard-capped by maxH;
// older entries scroll off the top.
func (m *Model) renderCodePanel(width, maxH int) string {
	if len(m.codeViews) == 0 || width < 20 || maxH < 5 {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render("Activity")
	// Total inner-content budget = panel height minus the rounded
	// border (2 lines top+bottom). Padding is (0, 1) so it adds no
	// vertical lines. The title and trailing blank consume 2 lines of
	// that budget; whatever's left is the entries window.
	innerH := maxH - 2
	if innerH < 3 {
		innerH = 3
	}
	bodyH := innerH - 2 // title + blank
	if bodyH < 1 {
		bodyH = 1
	}
	innerW := width - 4 // border + horizontal padding
	if innerW < 12 {
		innerW = 12
	}

	// Walk newest → oldest, accumulating rendered blocks until we've
	// used up bodyH. Each block: 1 header line + N preview lines + 1
	// blank separator (the separator is only counted between blocks,
	// so we subtract the trailing one when computing fit).
	type block struct{ lines []string }
	var blocks []block
	used := 0
	for i := len(m.codeViews) - 1; i >= 0; i-- {
		e := &m.codeViews[i]
		header := renderActivityHeader(*e, m.theme, innerW)
		preview := renderActivityPreview(e, m.theme, innerW)
		blockH := 1 + len(preview)
		sep := 0
		if len(blocks) > 0 {
			sep = 1
		}
		if used+sep+blockH > bodyH && len(blocks) > 0 {
			break
		}
		blocks = append(blocks, block{lines: append([]string{header}, preview...)})
		used += sep + blockH
	}

	// Reverse so oldest of the visible window renders first.
	rendered := make([]string, 0)
	for i := len(blocks) - 1; i >= 0; i-- {
		if i != len(blocks)-1 {
			rendered = append(rendered, "")
		}
		rendered = append(rendered, blocks[i].lines...)
	}
	hidden := len(m.codeViews) - len(blocks)
	titleRow := title
	if hidden > 0 {
		titleRow += "  " + lipgloss.NewStyle().Foreground(m.theme.Muted).
			Render(fmt.Sprintf("(%d older — Ctrl+P)", hidden))
	}
	bodyText := strings.Join(rendered, "\n")
	bodyLines := strings.Split(bodyText, "\n")
	// HARD truncate to bodyH so a too-tall block can never push the
	// panel past maxH. Without this the lipgloss Height() acts as a
	// MIN — overflow leaks through and makes the chat column flicker
	// up/down each frame as JoinHorizontal pads to the taller side.
	if len(bodyLines) > bodyH {
		bodyLines = bodyLines[len(bodyLines)-bodyH:]
	}
	inner := titleRow + "\n\n" + strings.Join(bodyLines, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent2).
		Padding(0, 1).
		Width(width - 2).
		Height(maxH - 2).
		MaxHeight(maxH).
		Render(inner)
}

// renderActivityHeader is the bold/colored top line for one entry —
// icon, path/label, and meta.
func renderActivityHeader(e codeViewEntry, t Theme, innerW int) string {
	icon := "📄"
	color := t.ReadIcon
	label := e.Path
	switch {
	case e.Thinking:
		icon = "💭"
		color = t.Thinking
		label = "thinking"
	case e.Tool != "":
		icon = "⚙"
		color = t.ToolIcon
		label = e.Tool
	case e.IsDiff:
		icon = "✏"
		color = t.EditIcon
	case e.IsNew:
		icon = "🆕"
		color = t.DiffAdd
	}
	maxLabel := innerW - 4 - 10 // icon + space + meta tail
	if maxLabel < 6 {
		maxLabel = 6
	}
	if len(label) > maxLabel {
		label = "…" + label[len(label)-maxLabel+1:]
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(color).Render(icon + " " + label)
	meta := lipgloss.NewStyle().Foreground(t.Muted).Faint(true).
		Render(" · " + e.When.Format("15:04:05") + "  " + e.Text)
	return head + meta
}

// renderActivityPreview returns 0..N indented preview lines for an
// entry. For thinking entries the full accumulated thought is soft-
// wrapped to the column width so long sentences read naturally across
// multiple rows. Cached per-entry so streaming thinking doesn't re-run
// wordWrap on every chat:thinking token.
func renderActivityPreview(e *codeViewEntry, t Theme, innerW int) []string {
	if e.cachedW == innerW && e.cachedThinkV == e.dirtyVer && e.cachedPreview != nil {
		return e.cachedPreview
	}
	style := lipgloss.NewStyle().Foreground(t.Muted)
	indent := "   "
	maxW := innerW - len(indent)
	if maxW < 8 {
		return nil
	}

	var rawLines []string
	var maxLines int
	if e.Thinking {
		if strings.TrimSpace(e.Content) == "" {
			e.cachedW, e.cachedThinkV, e.cachedPreview = innerW, e.dirtyVer, []string{}
			return nil
		}
		style = style.Italic(true)
		rawLines = wordWrap(e.Content, maxW)
		maxLines = 12
	} else {
		if e.Preview == "" {
			e.cachedW, e.cachedThinkV, e.cachedPreview = innerW, e.dirtyVer, []string{}
			return nil
		}
		for _, ln := range strings.Split(e.Preview, "\n") {
			if len(ln) > maxW {
				ln = ln[:maxW-1] + "…"
			}
			rawLines = append(rawLines, ln)
		}
		maxLines = len(rawLines)
	}
	if len(rawLines) > maxLines {
		rawLines = rawLines[len(rawLines)-maxLines:]
	}
	out := make([]string, 0, len(rawLines))
	for _, ln := range rawLines {
		out = append(out, style.Render(indent+ln))
	}
	e.cachedW, e.cachedThinkV, e.cachedPreview = innerW, e.dirtyVer, out
	return out
}

// wordWrap breaks s into lines no wider than maxW, preferring to split
// at word boundaries. Preserves intentional newlines in s. Falls back
// to hard splits for words that exceed maxW on their own.
func wordWrap(s string, maxW int) []string {
	if maxW <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, w := range words {
			// Word longer than the full row — break it hard.
			for len(w) > maxW {
				if cur != "" {
					out = append(out, cur)
					cur = ""
				}
				out = append(out, w[:maxW])
				w = w[maxW:]
			}
			if cur == "" {
				cur = w
				continue
			}
			if len(cur)+1+len(w) <= maxW {
				cur += " " + w
				continue
			}
			out = append(out, cur)
			cur = w
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// subagent panel — tracks subagent:* ws frames.
type subagentPanel struct {
	Tasks map[string]*subagentState
	Order []string
}

type subagentState struct {
	TaskID  string
	Title   string
	Status  string
	Lines   []string
	Updated time.Time

	// Progress fields populated from the richer subagent:* frame types
	// (iter, iter_done, tool_call, tool_start, tool_progress, heartbeat,
	// thinking, thinking_start, text, done). Previously the client only
	// handled start/line/log/done/error so all this progress info was
	// dropped on the floor and the panel sat at "(running)" forever.
	CurrentIter    int    // latest iteration number
	MaxIter        int    // cap on iteration count (from iter frames)
	CurrentTool    string // tool name currently executing, cleared on iter_done
	Elapsed        int    // seconds, from heartbeat
	ThinkingTokens int    // cumulative thinking tokens this iter
	StreamChars    int    // chars streamed this iter

	// Completion stats (populated on done/error).
	FinalElapsed  int
	FinalIters    int
	FinalTools    int
}

func newSubagentPanel() *subagentPanel {
	return &subagentPanel{Tasks: map[string]*subagentState{}}
}

func (m *Model) handleSubagentFrame(verb string, raw map[string]any) {
	if m.subagents == nil {
		m.subagents = newSubagentPanel()
	}
	id := asString(raw["taskId"], "")
	if id == "" {
		id = asString(raw["id"], "")
	}
	if id == "" {
		return
	}
	st, ok := m.subagents.Tasks[id]
	if !ok {
		st = &subagentState{TaskID: id, Status: "running"}
		m.subagents.Tasks[id] = st
		m.subagents.Order = append(m.subagents.Order, id)
	}
	st.Updated = time.Now()
	switch verb {
	case "start":
		st.Title = asString(raw["task"], asString(raw["title"], ""))
	case "iter":
		// New iteration begins — reset per-iter counters so the status
		// line reflects the current turn, not the cumulative total.
		st.CurrentIter = asInt(raw["iteration"], st.CurrentIter)
		st.MaxIter = asInt(raw["maxIter"], st.MaxIter)
		st.CurrentTool = ""
		st.ThinkingTokens = 0
		st.StreamChars = 0
	case "iter_done":
		// Iteration finished; clear the "currently running tool"
		// indicator so the status line doesn't say exec(...) while
		// the next iter starts thinking.
		st.CurrentTool = ""
	case "tool_start", "tool_call":
		st.CurrentTool = asString(raw["tool"], st.CurrentTool)
	case "tool_progress":
		// Tool still running — just bump Updated, no field change.
	case "heartbeat":
		st.CurrentIter = asInt(raw["iteration"], st.CurrentIter)
		st.Elapsed = asInt(raw["elapsed"], st.Elapsed)
		st.ThinkingTokens = asInt(raw["thinking"], st.ThinkingTokens)
		st.StreamChars = asInt(raw["chars"], st.StreamChars)
		if tool := asString(raw["toolName"], ""); tool != "" {
			st.CurrentTool = tool
		}
	case "thinking_start":
		st.CurrentTool = "thinking"
	case "thinking":
		st.ThinkingTokens = asInt(raw["tokens"], st.ThinkingTokens)
	case "text":
		// Streaming text — track chars for the status line. Ignore
		// the text body itself; we don't show it in the side panel.
		if t := asString(raw["text"], ""); t != "" {
			st.StreamChars += len(t)
		}
	case "line", "log":
		line := asString(raw["text"], asString(raw["line"], ""))
		if line != "" {
			st.Lines = append(st.Lines, line)
			if len(st.Lines) > 50 {
				st.Lines = st.Lines[len(st.Lines)-50:]
			}
		}
	case "done":
		st.Status = "done"
		st.CurrentTool = ""
		st.FinalElapsed = asInt(raw["elapsed"], st.Elapsed)
		st.FinalIters = asInt(raw["iterations"], st.CurrentIter)
		st.FinalTools = asInt(raw["toolCalls"], 0)
	case "error":
		st.Status = "error"
		st.CurrentTool = ""
	case "cancelled":
		st.Status = "cancelled"
		st.CurrentTool = ""
	}
}

// asInt extracts an integer from an arbitrary JSON value. Handles
// float64 (the default numeric type from encoding/json), int, and
// numeric strings. Returns `def` on anything unparseable.
func asInt(v any, def int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return def
}

// ── Plan-task panel — mirrors the subagent panel shape ──────────────
//
// Renders the checklist built during plan-mode execute turns. SPORE
// sends `task:create` + `task:update` frames when the agent calls
// `task_create` / `task_progress` during a plan execution. Status
// progression: pending → in_progress → done|error|cancelled.

type planTaskPanel struct {
	Tasks map[string]*planTaskState
	Order []string
}

type planTaskState struct {
	ID         string
	Subject    string
	Status     string // pending | in_progress | done | error | cancelled | blocked
	Note       string
	Updated    time.Time
	SessionKey string // for future multi-session filtering; not used yet
}

func newPlanTaskPanel() *planTaskPanel {
	return &planTaskPanel{Tasks: map[string]*planTaskState{}}
}

func (m *Model) handleTaskFrame(verb string, raw map[string]any) {
	if m.planTasks == nil {
		m.planTasks = newPlanTaskPanel()
	}
	id := asString(raw["id"], asString(raw["taskId"], ""))
	if id == "" {
		return
	}
	st, ok := m.planTasks.Tasks[id]
	if !ok {
		st = &planTaskState{ID: id, Status: "pending"}
		m.planTasks.Tasks[id] = st
		m.planTasks.Order = append(m.planTasks.Order, id)
	}
	st.Updated = time.Now()
	st.SessionKey = asString(raw["sessionKey"], st.SessionKey)
	switch verb {
	case "create":
		if s := asString(raw["subject"], ""); s != "" {
			st.Subject = s
		}
		if s := asString(raw["status"], ""); s != "" {
			st.Status = s
		}
	case "update":
		if s := asString(raw["status"], ""); s != "" {
			st.Status = s
		}
		// Prefer the more specific of note / result if present.
		if n := asString(raw["note"], ""); n != "" {
			st.Note = n
		} else if r := asString(raw["result"], ""); r != "" {
			st.Note = r
		}
		if s := asString(raw["subject"], ""); s != "" && st.Subject == "" {
			st.Subject = s
		}
	}
}

// prunePlanTasks — same contract as pruneSubagents: called on new
// chat:start; drops terminal rows older than 5s.
func (m *Model) prunePlanTasks() {
	if m.planTasks == nil {
		return
	}
	cutoff := time.Now().Add(-5 * time.Second)
	keep := m.planTasks.Order[:0]
	for _, id := range m.planTasks.Order {
		st := m.planTasks.Tasks[id]
		if st == nil {
			continue
		}
		if (st.Status == "done" || st.Status == "error" || st.Status == "cancelled") && st.Updated.Before(cutoff) {
			delete(m.planTasks.Tasks, id)
			continue
		}
		keep = append(keep, id)
	}
	m.planTasks.Order = keep
	if len(m.planTasks.Order) == 0 {
		m.planTasks = nil
	}
}

// renderPlanTasksPanel — bordered side panel showing the plan-mode
// execution checklist. Returns "" when there are no rows so the
// caller can lay out without reserving width.
func (m *Model) renderPlanTasksPanel(width, maxH int) string {
	if m.planTasks == nil || len(m.planTasks.Order) == 0 || width < 20 || maxH < 5 {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render("Plan Tasks")
	bodyH := maxH - 4
	if bodyH < 1 {
		bodyH = 1
	}
	// One line per task row; note line adds a 2nd line when present.
	// Budget 2 lines per task and take from the tail.
	perTask := 2
	maxTasks := bodyH / perTask
	if maxTasks < 1 {
		maxTasks = 1
	}
	start := len(m.planTasks.Order) - maxTasks
	if start < 0 {
		start = 0
	}
	var lines []string
	for _, id := range m.planTasks.Order[start:] {
		st := m.planTasks.Tasks[id]
		if st == nil {
			continue
		}
		icon := "◯"
		color := m.theme.Muted
		switch st.Status {
		case "in_progress":
			icon = "◐"
			color = m.theme.Accent
		case "done":
			icon = "✓"
			color = m.theme.Success
		case "error":
			icon = "✗"
			color = m.theme.Error
		case "cancelled":
			icon = "✕"
			color = m.theme.Muted
		case "blocked":
			icon = "⏸"
			color = m.theme.Muted
		}
		subject := st.Subject
		if subject == "" {
			subject = "(" + shortID(id) + ")"
		}
		row := trimTo(icon+" "+subject, width-4)
		lines = append(lines, lipgloss.NewStyle().Foreground(color).Bold(st.Status == "in_progress").Render(row))
		if st.Note != "" {
			note := trimTo(st.Note, width-6)
			lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Muted).Italic(true).Render("   "+note))
		}
	}
	more := ""
	if start > 0 {
		more = "\n" + lipgloss.NewStyle().Foreground(m.theme.Muted).Render(
			"  "+itoa(start)+" earlier hidden")
	}
	inner := title + "\n\n" + strings.Join(lines, "\n") + more
	innerLines := strings.Split(inner, "\n")
	if len(innerLines) > maxH-2 {
		innerLines = innerLines[len(innerLines)-(maxH-2):]
		inner = strings.Join(innerLines, "\n")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent2).
		Padding(0, 1).
		Width(width - 2).
		Height(maxH - 2).
		MaxHeight(maxH).
		Render(inner)
}

// pruneSubagents — called when a new user turn begins (chat:start).
// Removes tasks whose terminal status is older than 5 seconds so the
// panel doesn't show stale completed rows next to the new activity.
// If pruning empties the panel entirely, drop it so renderSubagentPanel
// returns "" and the chat column reclaims the width.
func (m *Model) pruneSubagents() {
	if m.subagents == nil {
		return
	}
	cutoff := time.Now().Add(-5 * time.Second)
	keep := m.subagents.Order[:0]
	for _, id := range m.subagents.Order {
		st := m.subagents.Tasks[id]
		if st == nil {
			continue
		}
		if (st.Status == "done" || st.Status == "error" || st.Status == "cancelled") && st.Updated.Before(cutoff) {
			delete(m.subagents.Tasks, id)
			continue
		}
		keep = append(keep, id)
	}
	m.subagents.Order = keep
	if len(m.subagents.Order) == 0 {
		m.subagents = nil
	}
}

func (m *Model) renderSubagentPanel(width, maxH int) string {
	if m.subagents == nil || len(m.subagents.Order) == 0 || width < 20 || maxH < 5 {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render("Subagents")
	bodyH := maxH - 4
	if bodyH < 1 {
		bodyH = 1
	}
	// Each task: 1 header line + 1 title line + (sometimes) 1 status line.
	// Budget 3 lines per task so rows don't clip mid-row.
	perTask := 3
	maxTasks := bodyH / perTask
	if maxTasks < 1 {
		maxTasks = 1
	}
	start := len(m.subagents.Order) - maxTasks
	if start < 0 {
		start = 0
	}
	var lines []string
	for _, id := range m.subagents.Order[start:] {
		st := m.subagents.Tasks[id]
		icon := "●"
		color := m.theme.Accent
		switch st.Status {
		case "done":
			icon = "✓"
			color = m.theme.Success
		case "error":
			icon = "✗"
			color = m.theme.Error
		case "cancelled":
			icon = "✕"
			color = m.theme.Muted
		}
		label := trimTo(st.Title, width-8)
		if label == "" {
			label = "(running)"
		}
		// Build the status line. For running tasks, show current iter,
		// current tool (or "thinking"), and elapsed seconds. For
		// terminal states, show the final iter/tool count + elapsed.
		var status string
		switch st.Status {
		case "done":
			if st.FinalIters > 0 || st.FinalElapsed > 0 {
				status = fmt.Sprintf("%d iters │ %d tools │ %ds", st.FinalIters, st.FinalTools, st.FinalElapsed)
			}
		case "error", "cancelled":
			if st.CurrentIter > 0 {
				status = fmt.Sprintf("stopped at iter %d │ %ds", st.CurrentIter, st.Elapsed)
			}
		default: // running
			var parts []string
			if st.CurrentIter > 0 {
				if st.MaxIter > 0 {
					parts = append(parts, fmt.Sprintf("iter %d/%d", st.CurrentIter, st.MaxIter))
				} else {
					parts = append(parts, fmt.Sprintf("iter %d", st.CurrentIter))
				}
			}
			if st.CurrentTool != "" {
				parts = append(parts, st.CurrentTool)
			} else if st.ThinkingTokens > 0 {
				parts = append(parts, fmt.Sprintf("thinking %d tok", st.ThinkingTokens))
			} else if st.StreamChars > 0 {
				parts = append(parts, fmt.Sprintf("writing %d ch", st.StreamChars))
			}
			if st.Elapsed > 0 {
				parts = append(parts, fmt.Sprintf("%ds", st.Elapsed))
			}
			status = strings.Join(parts, " │ ")
		}
		tag := lipgloss.NewStyle().Bold(true).Foreground(color).Render(icon + " " + shortID(id))
		lines = append(lines, tag, lipgloss.NewStyle().Foreground(m.theme.Muted).Render("   "+label))
		if status != "" {
			statusLine := trimTo(status, width-6)
			lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Muted).Italic(true).Render("   "+statusLine))
		}
	}
	more := ""
	if start > 0 {
		more = "\n" + lipgloss.NewStyle().Foreground(m.theme.Muted).Render(
			"  "+itoa(start)+" older hidden — Ctrl+P to expand")
	}
	inner := title + "\n\n" + strings.Join(lines, "\n") + more
	// Hard-clip to bodyH so a too-tall inner can never push the panel
	// past maxH and force the chat column to flicker up/down.
	innerLines := strings.Split(inner, "\n")
	if len(innerLines) > maxH-2 {
		innerLines = innerLines[len(innerLines)-(maxH-2):]
		inner = strings.Join(innerLines, "\n")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent2).
		Padding(0, 1).
		Width(width - 2).
		Height(maxH - 2).
		MaxHeight(maxH).
		Render(inner)
}

// renderExpandedPanel — full-screen activity browser (Ctrl+P view) with
// its own scrollable viewport + scrollbar. Up/down/pgup/pgdn scrolls.
func (m *Model) renderExpandedPanel() string {
	body := m.buildExpandedPanelBody()

	// Inner box dimensions after border + padding.
	innerW := m.width - 6 // 2 border + 4 horizontal padding
	if innerW < 10 {
		innerW = 10
	}
	innerH := m.height - 6 // 2 border + 2 vertical padding + 2 hint
	if innerH < 5 {
		innerH = 5
	}

	// Re-init viewport on first open or resize.
	if !m.panelViewInit || m.panelView.Width != innerW || m.panelView.Height != innerH {
		m.panelView = viewport.New(innerW, innerH)
		m.panelViewInit = true
	}
	m.panelView.SetContent(body)

	bar := scrollbar(&m.panelView, innerH, m.theme)
	content := lipgloss.JoinHorizontal(lipgloss.Top, m.panelView.View(), bar)

	hint := lipgloss.NewStyle().Foreground(m.theme.Muted).
		Render("↑↓/PgUp/PgDn scroll · Ctrl+P or Esc to close")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 2).
		Width(m.width - 2).
		Height(m.height - 2).
		Render(content + "\n" + hint)
}

// buildExpandedPanelBody composes the content string — separate function
// so the viewport can re-content when data changes.
func (m *Model) buildExpandedPanelBody() string {
	var sections []string
	codeTitle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(
		fmt.Sprintf("Code activity (%d events)", len(m.codeViews)))
	sections = append(sections, codeTitle)
	if len(m.codeViews) == 0 {
		sections = append(sections, lipgloss.NewStyle().Faint(true).Render("  (none yet)"))
	} else {
		for _, e := range m.codeViews {
			icon := "📄"
			if e.IsDiff {
				icon = "✏️ "
			} else if e.IsNew {
				icon = "🆕"
			}
			sections = append(sections, fmt.Sprintf("  %s %s · %s · %s",
				icon, e.Path, e.When.Format("15:04:05"), e.Text))
		}
	}
	sections = append(sections, "")
	saCount := 0
	if m.subagents != nil {
		saCount = len(m.subagents.Order)
	}
	saTitle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent2).Render(
		fmt.Sprintf("Subagents (%d tasks)", saCount))
	sections = append(sections, saTitle)
	if saCount == 0 {
		sections = append(sections, lipgloss.NewStyle().Faint(true).Render("  (none yet)"))
	} else {
		for _, id := range m.subagents.Order {
			st := m.subagents.Tasks[id]
			icon := "●"
			switch st.Status {
			case "done":
				icon = "✓"
			case "error":
				icon = "✗"
			case "cancelled":
				icon = "✕"
			}
			sections = append(sections, fmt.Sprintf("  %s %s — %s", icon, shortID(id), st.Title))
			for _, line := range st.Lines {
				sections = append(sections, lipgloss.NewStyle().Faint(true).Render("    "+trimTo(line, m.width-8)))
			}
		}
	}
	return strings.Join(sections, "\n")
}

func shortID(id string) string {
	if i := strings.LastIndex(id, "_"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func trimTo(s string, n int) string {
	if n <= 0 {
		return s
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}


func asString(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}
