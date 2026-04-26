package app

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// PermMode maps to Python's TuiPermissions modes.
type PermMode string

const (
	PermAuto   PermMode = "auto"
	PermAsk    PermMode = "ask"
	PermLocked PermMode = "locked"
	PermYolo   PermMode = "yolo"
)

// alwaysSafe — never need approval.
var alwaysSafe = map[string]bool{
	"read_file": true, "glob": true, "grep": true,
}

// dangerousPatterns — require approval even in auto. Mirrors
// acorn/permissions.py:DANGEROUS_PATTERNS.
var dangerousPatternsRe = compileAll([]string{
	`\brm\s+(-rf?|--recursive)`, `\brm\s+/`, `rmdir\s+/`,
	`\bmkfs\b`, `>\s*/dev/`, `dd\s+if=`,
	`chmod\s+(-R\s+)?777`, `chown\s+-R\s+.*/`,
	`\bgit\s+push\s+.*--force`, `\bgit\s+reset\s+--hard`,
	`\bdrop\s+table\b`, `\bdrop\s+database\b`,
	`\btruncate\s+table\b`,
	`\bmkfs\.\w+\b`, `\bfdisk\b`, `\bparted\b`,
	`:\(\)\{`, `curl.*\|\s*(ba)?sh`, `wget.*\|\s*(ba)?sh`,
	`\bkill\s+-9\b`,
	`\bdel\s+/[sq]`, `\brd\s+/s`, `\brmdir\s+/s`,
	`\bformat\s+[a-zA-Z]:`, `\bdiskpart\b`,
	`Remove-Item.*-Recurse.*-Force`, `Stop-Process.*-Force`,
	`Clear-Content.*-Force`, `Set-ExecutionPolicy\s+Unrestricted`,
})

func compileAll(ps []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(ps))
	for _, p := range ps {
		out = append(out, regexp.MustCompile("(?i)"+p))
	}
	return out
}

// IsDangerous checks whether a tool call matches dangerous patterns.
func IsDangerous(name string, input map[string]any) bool {
	if name == "exec" {
		cmd, _ := input["command"].(string)
		for _, r := range dangerousPatternsRe {
			if r.FindStringIndex(cmd) != nil {
				return true
			}
		}
	}
	if name == "write_file" {
		p, _ := input["path"].(string)
		if strings.HasPrefix(p, "/etc/") || strings.HasPrefix(p, "/usr/") || strings.HasPrefix(p, "/bin/") {
			return true
		}
		lp := strings.ReplaceAll(strings.ToLower(p), "/", `\`)
		for _, w := range []string{`c:\windows`, `c:\program files`, `c:\programdata`} {
			if strings.HasPrefix(lp, w) {
				return true
			}
		}
	}
	return false
}

// Summarize gives a one-line human label for a tool call.
func Summarize(name string, input map[string]any) string {
	switch name {
	case "exec":
		c, _ := input["command"].(string)
		if len(c) > 120 {
			c = c[:120] + "…"
		}
		return c
	case "write_file", "edit_file", "read_file":
		p, _ := input["path"].(string)
		return p
	case "web_fetch":
		u, _ := input["url"].(string)
		if len(u) > 100 {
			u = u[:100]
		}
		return u
	case "web_serve":
		if v, ok := input["dir"].(string); ok && v != "" {
			return v
		}
		if v, ok := input["directory"].(string); ok && v != "" {
			return v
		}
		return ""
	}
	return fmt.Sprintf("%v", input)
}

// MakeRule derives a session allow-rule from a tool call. Mirrors Python.
func MakeRule(name string, input map[string]any) string {
	switch name {
	case "exec":
		cmd, _ := input["command"].(string)
		cmd = strings.TrimSpace(cmd)
		parts := strings.Fields(cmd)
		first := ""
		if len(parts) > 0 {
			first = parts[0]
			if i := strings.LastIndex(first, "/"); i >= 0 {
				first = first[i+1:]
			}
		}
		if first == "" {
			return "exec:*"
		}
		return "exec:" + first + "*"
	case "write_file", "edit_file":
		p, _ := input["path"].(string)
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return name + ":" + p[:i] + "/*"
		}
		return name + ":*"
	}
	return name + ":*"
}

// MatchesRule — true if the rule covers this tool call.
func MatchesRule(rule, name string, input map[string]any) bool {
	colon := strings.Index(rule, ":")
	if colon < 0 {
		return rule == name
	}
	if rule[:colon] != name {
		return false
	}
	pat := rule[colon+1:]
	if pat == "*" {
		return true
	}
	switch name {
	case "exec":
		cmd, _ := input["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if strings.HasSuffix(pat, "*") {
			prefix := strings.TrimSuffix(pat, "*")
			if strings.HasPrefix(cmd, prefix) {
				return true
			}
			first := ""
			if parts := strings.Fields(cmd); len(parts) > 0 {
				first = parts[0]
			}
			return first == strings.TrimRight(prefix, " ")
		}
		return strings.HasPrefix(cmd, pat)
	case "write_file", "edit_file":
		p, _ := input["path"].(string)
		if strings.HasSuffix(pat, "/*") {
			dir := strings.TrimSuffix(pat, "/*")
			return strings.HasPrefix(p, dir+"/")
		}
		return strings.HasPrefix(p, pat)
	}
	return false
}

// TUIPerms implements tools.Permissions backed by a modal on the Model.
// Approval goes through a blocking channel so we can open the modal on the
// UI thread and wait for the keystroke result.
type TUIPerms struct {
	m    *Model
	mu   sync.Mutex
	mode PermMode
	// Session-scope allow rules.
	rules []string
	// Pending prompt — a goroutine waits on ch for the UI to resolve.
	pendingCh chan bool
	pendingName  string
	pendingInput map[string]any
	pendingRule  string
}

func newTUIPerms(m *Model) *TUIPerms {
	return &TUIPerms{m: m, mode: PermAsk}
}

// SetMode switches the overall approval posture.
func (p *TUIPerms) SetMode(mode PermMode) {
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

// Mode returns the current permission mode.
func (p *TUIPerms) Mode() PermMode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

// AddRule grows the session allow list (e.g. "exec:git*").
func (p *TUIPerms) AddRule(r string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.rules {
		if e == r {
			return
		}
	}
	p.rules = append(p.rules, r)
}

// Rules returns a copy of the current allow list.
func (p *TUIPerms) Rules() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.rules))
	copy(out, p.rules)
	return out
}

// IsAutoApproved returns true if the tool call doesn't need a prompt.
func (p *TUIPerms) IsAutoApproved(name string, input map[string]any) bool {
	p.mu.Lock()
	mode := p.mode
	rules := append([]string(nil), p.rules...)
	p.mu.Unlock()
	if alwaysSafe[name] {
		return true
	}
	if mode == PermLocked {
		return false
	}
	if mode == PermYolo {
		return true
	}
	if mode == PermAuto {
		if IsDangerous(name, input) {
			return false
		}
		return true
	}
	// PermAsk — check session rules.
	for _, r := range rules {
		if MatchesRule(r, name, input) {
			return true
		}
	}
	return false
}

// Prompt opens the permission modal and blocks until the user decides.
// Runs off the Tea goroutine — the UI signals result via m.resolvePerm().
func (p *TUIPerms) Prompt(name string, input map[string]any) bool {
	// In yolo mode, never prompt. In locked mode, always deny without asking.
	p.mu.Lock()
	mode := p.mode
	p.mu.Unlock()
	if mode == PermYolo {
		return true
	}
	if mode == PermLocked {
		return false
	}
	ch := make(chan bool, 1)
	p.mu.Lock()
	p.pendingCh = ch
	p.pendingName = name
	p.pendingInput = input
	p.pendingRule = MakeRule(name, input)
	p.mu.Unlock()

	// Signal the Tea program to open the modal.
	if p.m != nil && p.m.sendProgramMsg != nil {
		p.m.sendProgramMsg(openPermModalMsg{
			name:    name,
			summary: Summarize(name, input),
			dangerous: IsDangerous(name, input),
			rule:    p.pendingRule,
		})
	}

	// Broadcast to observers (mobile / VS Code companion) so they can
	// render the same modal or at least show "operator approval needed"
	// state. Mirrors acorn/permissions.py:142. Safe off the Tea
	// goroutine: m.Broadcast → conn.Client.Send is mutex-locked.
	if p.m != nil {
		p.m.Broadcast("tool:awaiting-approval", map[string]any{
			"tool":      name,
			"summary":   Summarize(name, input),
			"dangerous": IsDangerous(name, input),
			"rule":      p.pendingRule,
		})
	}

	allowed := <-ch
	p.mu.Lock()
	p.pendingCh = nil
	p.pendingName = ""
	p.pendingInput = nil
	p.pendingRule = ""
	p.mu.Unlock()
	// Companion gets the resolution too so it can dismiss its prompt UI.
	if p.m != nil {
		p.m.Broadcast("tool:approval-resolved", map[string]any{
			"tool":    name,
			"allowed": allowed,
		})
	}
	return allowed
}

// resolvePerm is called from the UI thread to answer the pending prompt.
func (p *TUIPerms) resolvePerm(allowed bool, addRule bool) {
	p.mu.Lock()
	ch := p.pendingCh
	rule := p.pendingRule
	p.mu.Unlock()
	if ch == nil {
		return
	}
	if allowed && addRule && rule != "" {
		p.AddRule(rule)
	}
	select {
	case ch <- allowed:
	default:
	}
}

// openPermModalMsg is delivered to the Bubble Tea program when the
// permissions layer wants to open a modal. The UI thread picks it up,
// flips m.modal = modalPermission, stashes the data, and re-renders.
type openPermModalMsg struct {
	name, summary, rule string
	dangerous           bool
}
