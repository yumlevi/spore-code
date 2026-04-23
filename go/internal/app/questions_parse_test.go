package app

import (
	"strings"
	"testing"
)

// Regression for the "blank lines between numbered items truncated
// the question set to 1" bug that shipped in v0.1.11. Real assistant
// output captured from sporebfl's session DB after the user reported
// "didn't show me the question ui in acorn" on v0.1.12.
func TestParseQuestionsBlock_blankLinesBetweenItems(t *testing.T) {
	in := `Good idea. "A website about Claude" branches in a bunch of directions — a comparison tool, a fan/tribute page, a documentation hub, or something more creative.

**QUESTIONS:**

1. **What's the angle?** Compare Claude to other LLMs, showcase what Claude can do with demos, a creative tribute/portrait, or document its history and capabilities?

2. **What's the tone?** Playful and fan-like, clean and editorial (like a product page), or technical and informational?

3. **Any interactive features?** Live chat demo, prompt playground, model comparison table, or static content only?
`
	qs := parseQuestionsBlock(in)
	if len(qs) != 3 {
		t.Fatalf("expected 3 questions, got %d: %#v", len(qs), qs)
	}
	if qs[0].Multi || qs[1].Multi || qs[2].Multi {
		t.Errorf("none of these should be multi-select")
	}
	for i, q := range qs {
		if q.Text == "" {
			t.Errorf("question %d has empty text", i)
		}
	}
}

func TestParseQuestionsBlock_jsonFenced(t *testing.T) {
	in := "let me know:\n\nQUESTIONS:\n```json\n[\n  {\"text\": \"Framework?\", \"type\": \"single\", \"options\": [\"React\", \"Vue\"]},\n  {\"text\": \"Features?\", \"type\": \"multi\", \"options\": [\"Auth\", \"DB\"]},\n  {\"text\": \"Project name?\", \"type\": \"open\"}\n]\n```\n"
	qs := parseQuestionsBlock(in)
	if len(qs) != 3 {
		t.Fatalf("expected 3 questions, got %d: %#v", len(qs), qs)
	}
	if qs[0].Multi {
		t.Errorf("q0 should be single-select")
	}
	if !qs[1].Multi {
		t.Errorf("q1 should be multi-select")
	}
	if len(qs[2].Options) != 0 {
		t.Errorf("q2 should be open-ended")
	}
}

// Real captured turn where the agent gave up on QUESTIONS: and just
// wrote prose options with **Option A** / **Option B** / **Option C**
// ending in "Which one? Or mix two together?". Parser fallback should
// synthesize a picker.
func TestParseQuestionsBlock_inlineOptionsFallback(t *testing.T) {
	in := `We've danced this twice already — let me stop asking and just propose something.

**Option A: "Good Dogs"** — Single-page editorial, warm palette, 6–8 breeds with personality blurbs, scroll-triggered reveals. Like the bridges site but fluffier. Static, no API calls.

**Option B: "Should You Get a Dog?"** — Interactive personality quiz: asks about your lifestyle, living situation, activity level → recommends a breed. Fun, shareable, still static data.

**Option C: "Dogepedia"** — Filterable breed grid with search, size filters, temperament tags. Small vanilla JS for interactivity. Good if you want a mini-app feel.

All three are single-page, no build step, served from ` + "`/mnt/user/appdata/anima/test`" + `.

**Which one?** Or mix two together?
`
	qs := parseQuestionsBlock(in)
	if len(qs) != 1 {
		t.Fatalf("expected 1 synthesized question, got %d: %#v", len(qs), qs)
	}
	if len(qs[0].Options) < 3 {
		t.Errorf("expected ≥3 options, got %d: %v", len(qs[0].Options), qs[0].Options)
	}
}

// "Which direction?" + numbered options + "Any preference?" — the
// other variant the user hit. No "Option N" prefix, no QUESTIONS:
// marker, just a numbered list with a closing question.
func TestParseQuestionsBlock_inlineOptionsNumbered(t *testing.T) {
	in := `Baseball covers a lot of ground. A retro-themed baseball card browser, a live MLB scoreboard with stats, a fantasy league dashboard, or a sabermetrics playground — all very different builds.

Which direction?

1. MLB Scoreboard + Standings — Live scores, team records, player stats. Needs an API (newsapi, ESPN, or MLB's free GUMBO endpoint).
2. Baseball History Encyclopedia — Iconic players, teams, moments — static, editorial, like the bridges/dog sites.
3. Fantasy Dashboard — Track your roster, compare player stats, projected scoring. All static data unless you wire a real league API.
4. Retro Baseball Card Browser — Flip cards showing player stats with vintage design. Pure front-end, no API needed.

Any preference?
`
	qs := parseQuestionsBlock(in)
	if len(qs) != 1 {
		t.Fatalf("expected 1 synthesized question, got %d: %#v", len(qs), qs)
	}
	if len(qs[0].Options) != 4 {
		t.Errorf("expected 4 options, got %d: %v", len(qs[0].Options), qs[0].Options)
	}
	// Labels should be the short part before the em-dash.
	wantPrefix := []string{"MLB Scoreboard", "Baseball History", "Fantasy Dashboard", "Retro Baseball Card"}
	for i, want := range wantPrefix {
		if i >= len(qs[0].Options) {
			break
		}
		if !strings.HasPrefix(qs[0].Options[i], want) {
			t.Errorf("opt[%d] = %q, want prefix %q", i, qs[0].Options[i], want)
		}
	}
}

func TestParseQuestionsBlock_proseSingleAndMulti(t *testing.T) {
	in := `QUESTIONS:
1. Framework? [React / Vue / Svelte]
2. Features? {Auth / DB / API}
3. Project name?
`
	qs := parseQuestionsBlock(in)
	if len(qs) != 3 {
		t.Fatalf("expected 3, got %d", len(qs))
	}
	if len(qs[0].Options) != 3 || qs[0].Multi {
		t.Errorf("q0: want single 3 opts, got multi=%v opts=%v", qs[0].Multi, qs[0].Options)
	}
	if !qs[1].Multi {
		t.Errorf("q1: want multi")
	}
	if len(qs[2].Options) != 0 {
		t.Errorf("q2: want open-ended")
	}
}
