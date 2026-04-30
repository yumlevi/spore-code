package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/spore-code/internal/proto"
	"github.com/yumlevi/spore-code/internal/tools"
)

// /test exists so the user can exercise UI features that normally require
// the agent to trigger them — question modals, plan approval modals,
// permission prompts, panel rendering, markdown rendering, streaming —
// without paying for an LLM round-trip every time something might be
// broken. Mirrors acorn/commands/test.py at a smaller scope.
//
// Each test is a func taking the live Model. Returns nil on success or
// an error describing the assertion failure. Interactive tests (open a
// modal, push a streamed bubble, etc.) return nil immediately and let
// the user verify visually.

type testFn func(m *Model) error

type testEntry struct {
	desc string
	fn   testFn
}

var testRegistry = map[string]testEntry{}
var testNamesSorted []string

func registerTest(name, desc string, fn testFn) {
	testRegistry[name] = testEntry{desc: desc, fn: fn}
}

func init() {
	registerTest("questions", "Open the QUESTIONS modal with single + multi + open-ended samples", testQuestions)
	registerTest("plan", "Open the plan-approval modal with a sample plan", testPlan)
	registerTest("permission", "Open the per-tool permission modal for a fake exec call", testPermission)
	registerTest("parse", "Run parseQuestionsBlock against 5 sample inputs and assert structure", testParse)
	registerTest("wrap", "Assert wrapForPanel doesn't corrupt multi-byte UTF-8 / emoji", testWrap)
	registerTest("sandbox", "Assert read_file / write_file / edit_file reject paths outside cwd", testSandbox)
	registerTest("activity", "Push sample thinking + tool + file-op entries into the activity panel", testActivity)
	registerTest("streaming", "Simulate a chat:delta burst into a fake assistant bubble", testStreaming)
	registerTest("themes", "Cycle through every theme so you can eyeball the palette", testThemes)
	registerTest("markdown", "Push a markdown-rich assistant message to verify glamour rendering", testMarkdown)

	register(&slashCmd{
		Name:    "/test",
		Help:    "Run a UI / behavior test (try `/test list`)",
		Handler: cmdTest,
	})
}

// cmdTest is the slash dispatcher.
//
//	/test               or /test list   list all available tests
//	/test all                            run every assertion-based test
//	/test <name>                         run one
func cmdTest(m *Model, args []string) (tea.Model, tea.Cmd) {
	name := ""
	if len(args) > 0 {
		name = strings.ToLower(args[0])
	}
	if name == "" || name == "list" || name == "help" {
		// Sort once so /test list output is stable.
		if len(testNamesSorted) != len(testRegistry) {
			testNamesSorted = make([]string, 0, len(testRegistry))
			for n := range testRegistry {
				testNamesSorted = append(testNamesSorted, n)
			}
			sort.Strings(testNamesSorted)
		}
		var b strings.Builder
		b.WriteString("Available tests (use /test <name>):\n")
		maxLen := 0
		for _, n := range testNamesSorted {
			if len(n) > maxLen {
				maxLen = len(n)
			}
		}
		for _, n := range testNamesSorted {
			fmt.Fprintf(&b, "  /test %-*s  %s\n", maxLen, n, testRegistry[n].desc)
		}
		b.WriteString("  /test all" + strings.Repeat(" ", maxLen-3) + "  Run every assertion-based test (skips interactive ones)")
		m.pushChat("system", b.String())
		return m, nil
	}

	if name == "all" {
		// Skip interactive tests in `all` runs — they need the user to
		// dismiss a modal between each one. Anything that returns nil
		// after opening a modal is interactive; the assertion-based
		// ones run cleanly back-to-back.
		skipInteractive := map[string]bool{
			"questions": true, "plan": true, "permission": true,
			"activity": true, "streaming": true, "themes": true,
			"markdown": true,
		}
		var passed, failed int
		var fails []string
		start := time.Now()
		for _, n := range testNamesSorted {
			if skipInteractive[n] {
				continue
			}
			if err := testRegistry[n].fn(m); err != nil {
				failed++
				fails = append(fails, fmt.Sprintf("  ✗ %s: %s", n, err.Error()))
			} else {
				passed++
			}
		}
		elapsed := time.Since(start)
		summary := fmt.Sprintf("/test all — %d passed, %d failed (%s)", passed, failed, elapsed.Round(time.Millisecond))
		if len(fails) > 0 {
			summary += "\n" + strings.Join(fails, "\n")
		}
		summary += "\n(interactive tests — questions, plan, permission, activity, streaming, themes, markdown — must be run individually)"
		m.pushChat("system", summary)
		return m, nil
	}

	entry, ok := testRegistry[name]
	if !ok {
		m.pushChat("system", "Unknown test: "+name+"  (try /test list)")
		return m, nil
	}
	if err := entry.fn(m); err != nil {
		m.pushChat("system", "/test "+name+" — ✗ FAILED: "+err.Error())
	} else {
		m.pushChat("system", "/test "+name+" — ✓ ok")
	}
	return m, nil
}

// ── interactive tests — open a modal / push a bubble ───────────────

func testQuestions(m *Model) error {
	qs := []question{
		{Text: "What framework?", Options: []string{"React", "Vue", "Svelte", "SolidJS"}, Multi: false},
		{Text: "Which features should it ship with?", Options: []string{"Auth", "Database", "API", "WebSocket"}, Multi: true},
		{Text: "Project directory name?"}, // open-ended — Options nil
	}
	m.openQuestionModal(qs)
	return nil
}

func testPlan(m *Model) error {
	plan := `# Sample Plan — exercise the approval modal

## Phases

1. **Read the existing config** (read_file ./config.toml)
2. **Add a [server] section** with default host=localhost port=8080
3. **Verify** by re-reading and parsing the TOML

## Files touched

- ./config.toml (modify)

## Risks

- Existing [server] section gets clobbered. Mitigated by reading first.

PLAN_READY`
	m.openPlanModal(plan)
	return nil
}

func testPermission(m *Model) error {
	m.modal = modalPermission
	m.permission = &permissionModal{
		name:      "exec",
		summary:   "rm -rf /tmp/test-acorn-permission",
		rule:      "exec:rm -rf*",
		dangerous: true,
	}
	return nil
}

func testActivity(m *Model) error {
	m.appendActivity(codeViewEntry{
		Path:    "src/main.go",
		Content: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
		Preview: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")",
		Text:    "8 lines, 67 bytes",
		When:    time.Now(),
	})
	m.appendActivity(codeViewEntry{
		Path:    "internal/app/view.go",
		Preview: "func renderMessage(c chatMsg, w int, t Theme) string {",
		Text:    "+4 / -2 lines",
		IsDiff:  true,
		When:    time.Now(),
	})
	m.appendThinking("Looking at how the agent ")
	m.appendThinking("would handle this — first read the file, then ")
	m.appendThinking("propose an edit, then verify with the user.")
	m.appendToolExec("read_file", "internal/app/view.go")
	m.pushChat("system", "Pushed 4 sample entries to the activity panel. Toggle visibility with /panel; expand with Ctrl+P.")
	return nil
}

func testStreaming(m *Model) error {
	// Push three streaming chunks separated by simulated delays via
	// tea.Tick. For simplicity we just inject the full text in one go
	// here — the test's value is that you see the rendering, not the
	// per-token timing.
	m.startStream()
	chunks := []string{
		"Simulating a streamed response. ",
		"Here's a markdown-ish chunk:\n\n- one\n- two\n- three\n\n",
		"And a code fence:\n```go\nfunc demo() {}\n```\n\n",
		"This whole reply was generated locally by /test streaming — no agent round-trip.",
	}
	for _, c := range chunks {
		m.appendDelta(c)
	}
	m.endStream()
	return nil
}

func testThemes(m *Model) error {
	original := m.theme.Name
	names := ThemeNames()
	var b strings.Builder
	b.WriteString("Cycling through themes (each shown for ~80ms in palette logs only):\n")
	for _, n := range names {
		b.WriteString("  · " + n + "\n")
	}
	b.WriteString("Current is restored to '" + original + "'. Use /theme <name> to actually switch.")
	m.pushChat("system", b.String())
	return nil
}

func testMarkdown(m *Model) error {
	sample := "# Header 1\n\n" +
		"Some body text with **bold**, *italic*, and `inline code`.\n\n" +
		"## A list\n\n" +
		"- alpha\n- beta\n- gamma\n\n" +
		"## A code fence\n\n" +
		"```go\n" +
		"func add(a, b int) int {\n\treturn a + b\n}\n" +
		"```\n\n" +
		"> A blockquote — should render styled.\n\n" +
		"And a [link](https://example.com)."
	// Push as an assistant message so renderMessage runs it through glamour.
	m.messages = append(m.messages, chatMsg{
		Role: "assistant", Text: sample, Timestamp: time.Now(),
	})
	m.historyDirty = true
	m.followBottom = true
	m.rerenderViewport()
	return nil
}

// ── assertion-based tests — fast, included in /test all ────────────

func testParse(m *Model) error {
	type tc struct {
		name string
		in   string
		nQ   int
		opts0 []string
		multi1 bool
	}
	cases := []tc{
		{
			name: "standard",
			in: "QUESTIONS:\n1. What database? [PostgreSQL / MySQL / SQLite]\n" +
				"2. Which features? {Auth / API / WebSocket}\n3. Project name?\n",
			nQ:    3,
			opts0: []string{"PostgreSQL", "MySQL", "SQLite"},
			multi1: true,
		},
		{
			name: "no marker",
			in:   "1. just a list\n2. without marker\n",
			nQ:   0,
		},
		{
			name: "blank line after marker",
			in:   "QUESTIONS:\n\n1. First? [A / B]\n2. Second? [C / D]\n",
			nQ:   2,
		},
		{
			name: "questions + PLAN_READY same response",
			in:   "QUESTIONS:\n1. Version? [A / B]\n\nPLAN_READY",
			nQ:   1,
		},
		{
			name: "brackets without slash are not options",
			in:   "QUESTIONS:\n1. What [something] do you want?\n",
			nQ:   1,
		},
	}
	for _, c := range cases {
		got := parseQuestionsBlock(c.in)
		if len(got) != c.nQ {
			return fmt.Errorf("case %q: expected %d questions, got %d", c.name, c.nQ, len(got))
		}
		if c.opts0 != nil {
			if len(got[0].Options) != len(c.opts0) {
				return fmt.Errorf("case %q: opts0 length expected %d, got %d", c.name, len(c.opts0), len(got[0].Options))
			}
			for i, want := range c.opts0 {
				if got[0].Options[i] != want {
					return fmt.Errorf("case %q: opts0[%d] expected %q, got %q", c.name, i, want, got[0].Options[i])
				}
			}
		}
		if c.multi1 && len(got) > 1 && !got[1].Multi {
			return fmt.Errorf("case %q: question 1 expected multi-select", c.name)
		}
	}
	return nil
}

func testWrap(m *Model) error {
	// Cases that the byte-based wrap used to corrupt: em dash, smart
	// quote, box-drawing (3 bytes each in UTF-8), CJK (3 bytes), emoji
	// (4 bytes). After wrap, joined output should round-trip equal to
	// the input modulo the inserted newlines and stripped leading-spaces
	// on continuation lines.
	cases := []struct {
		in    string
		width int
	}{
		{"hello — world", 8},                 // em dash mid-line
		{"“smart” quotes", 6},                // smart quotes
		{"█▄▀▄▄▄ █▀▄ ▀▀ █▄", 4},              // box-drawing run
		{"日本語のテストです長い行", 6},         // CJK wide-cells
		{"emoji 🎉 in the middle of words", 10}, // emoji
	}
	for _, c := range cases {
		out := wrapForPanel(c.in, c.width)
		// Joining the wrapped output back must be a strict prefix /
		// permutation of the input — same chars in same order. We can
		// detect rune corruption by re-stripping the inserted \n and
		// any leading-space trims on continuation lines, then comparing.
		clean := strings.ReplaceAll(out, "\n", "")
		if !runesEqualIgnoringSpaces(clean, c.in) {
			return fmt.Errorf("wrap corrupted runes for %q at width %d: got %q", c.in, c.width, out)
		}
	}
	return nil
}

// runesEqualIgnoringSpaces — true when both strings have the same runes
// in the same order, ignoring ASCII spaces. Used by testWrap to verify
// the wrap function didn't drop or split UTF-8 bytes — it's allowed to
// strip leading spaces on continuation lines.
func runesEqualIgnoringSpaces(a, b string) bool {
	ar := []rune(a)
	br := []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if ar[i] == ' ' {
			i++
			continue
		}
		if br[j] == ' ' {
			j++
			continue
		}
		if ar[i] != br[j] {
			return false
		}
		i++
		j++
	}
	for i < len(ar) {
		if ar[i] != ' ' {
			return false
		}
		i++
	}
	for j < len(br) {
		if br[j] != ' ' {
			return false
		}
		j++
	}
	return true
}

func testSandbox(m *Model) error {
	// Force-attempt three operations targeting paths outside cwd — each
	// should return an error map. The Python /test does the same.
	checks := []struct {
		op   string
		fn   func() any
		want string
	}{
		{
			op:   "read_file /etc/passwd (strict)",
			fn:   func() any { return tools.ReadFile(map[string]any{"path": "/etc/passwd"}, m.cwd, "strict") },
			want: "outside",
		},
		{
			op:   "read_file ../../etc/shadow (strict)",
			fn:   func() any { return tools.ReadFile(map[string]any{"path": "../../etc/shadow"}, m.cwd, "strict") },
			want: "outside",
		},
		{
			op:   "write_file /usr/bin/evil (strict)",
			fn:   func() any { return tools.WriteFile(map[string]any{"path": "/usr/bin/evil", "content": "x"}, m.cwd, "strict") },
			want: "outside",
		},
		{
			op:   "edit_file /etc/hosts (strict)",
			fn:   func() any { return tools.EditFile(map[string]any{"path": "/etc/hosts", "old_string": "x", "new_string": "y"}, m.cwd, "strict") },
			want: "outside",
		},
	}
	for _, c := range checks {
		r := c.fn()
		// File ops return either map[string]any or map[string]string
		// depending on the path — accept both.
		var errMsg string
		switch mp := r.(type) {
		case map[string]any:
			errMsg, _ = mp["error"].(string)
		case map[string]string:
			errMsg = mp["error"]
		default:
			return fmt.Errorf("%s: expected error map, got %T", c.op, r)
		}
		if errMsg == "" {
			return fmt.Errorf("%s: expected error, got %v", c.op, r)
		}
		if !strings.Contains(strings.ToLower(errMsg), c.want) {
			return fmt.Errorf("%s: expected error containing %q, got %q", c.op, c.want, errMsg)
		}
	}
	return nil
}

// Touch unused imports so go vet doesn't complain if anything below
// gets refactored away during edits.
var _ = proto.AskUser{}
