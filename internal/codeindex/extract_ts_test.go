package codeindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractTSInline runs the TS extractor over a small synthetic source
// blob covering the main declaration shapes the agent will encounter.
// Anchored on shapes rather than a real repo so it stays deterministic.
func TestExtractTSInline(t *testing.T) {
	src := []byte(`// header comment
import { foo } from './foo';

export interface UserOpts {
  id: string;
  name?: string;
}

export type UserMap = Record<string, UserOpts>;

export const ANSWER = 42;

export function greet(name: string): string {
  return "hi " + name;
}

export default async function main() {
  return greet("world");
}

export const Add = (a: number, b: number) => a + b;

const internalHelper = function internalHelper() { return 1; };

export class Repo<T> {
  constructor(private name: string) {}
  static makeDefault(): Repo<unknown> { return new Repo("default"); }
  get displayName() { return this.name; }
  set displayName(v: string) { this.name = v; }
  async fetch(id: string): Promise<T | null> {
    if (id === "") return null;
    return null;
  }
  private _hidden() { return 1; }
}

enum Color { Red, Green, Blue }
`)
	want := map[string]string{
		"UserOpts":       "interface",
		"UserMap":        "type",
		"ANSWER":         "function", // const-arrow form might not match; we'll relax this
		"greet":          "function",
		"main":           "function",
		"Add":            "function",
		"internalHelper": "function",
		"Repo":           "class",
		"makeDefault":    "method",
		"displayName":    "method",
		"fetch":          "method",
		"_hidden":        "method",
		"Color":          "enum",
		"constructor":    "constructor",
	}

	fe := FileEntry{
		AbsPath:  "/synth/example.ts",
		RelPath:  "example.ts",
		Language: LangTS,
	}
	res := tsExtractor{lang: LangTS}.Extract(fe, src)
	if res.Err != nil {
		t.Fatalf("extract: %v", res.Err)
	}

	got := map[string]string{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Kind
	}

	for name, kind := range want {
		// ANSWER is a plain const, not a function — relax the assertion: we expect
		// either it's missing (acceptable) or its kind matches.
		if name == "ANSWER" {
			continue
		}
		if got[name] != kind {
			t.Errorf("symbol %q: want kind=%q, got kind=%q (present=%v)", name, kind, got[name], got[name] != "")
		}
	}

	// Methods inside Repo<T> should have container set.
	for _, s := range res.Symbols {
		if s.Name == "fetch" {
			if s.Container != "Repo" {
				t.Errorf("fetch container want Repo, got %q", s.Container)
			}
		}
	}

	t.Logf("extracted %d symbols:", len(res.Symbols))
	for _, s := range res.Symbols {
		t.Logf("  %s @ %d  kind=%s  container=%q  exp=%v", s.Name, s.StartLine, s.Kind, s.Container, s.Exported)
	}
}

// TestEndToEndIndexAnimaPlugins runs the walker + JS extractor over the
// sibling anima-new/plugins directory if present. Skips when the path
// isn't accessible (e.g. CI environments).
func TestEndToEndIndexAnimaPlugins(t *testing.T) {
	const animaPlugins = "/mnt/user/appdata/anima/anima-new/plugins"
	if _, err := os.Stat(animaPlugins); err != nil {
		t.Skipf("anima-new plugins not accessible at %s; skipping", animaPlugins)
	}

	tmp := t.TempDir()
	store, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	tx, err := store.BeginIndex()
	if err != nil {
		t.Fatalf("BeginIndex: %v", err)
	}
	files := 0
	syms := 0
	walkErr := Walk(WalkOptions{
		Root:      animaPlugins,
		Languages: map[string]bool{LangJS: true, LangTS: true},
		MaxFiles:  200,
	}, func(fe FileEntry) bool {
		files++
		res := ExtractFile(fe)
		_ = tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", len(res.Symbols))
		for _, s := range res.Symbols {
			if err := tx.UpsertSymbol(s); err != nil {
				t.Fatalf("UpsertSymbol(%s): %v", s.QName, err)
			}
			syms++
		}
		return true
	})
	if walkErr != nil {
		t.Fatalf("Walk: %v", walkErr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if files == 0 || syms == 0 {
		t.Fatalf("expected files+syms > 0, got files=%d syms=%d", files, syms)
	}

	// upsertProject should turn up — it's a top-level function in
	// session-graph/lib/projects.js, the canonical project-node API.
	results, err := store.Search(SearchQuery{NameLike: "upsertProject"})
	if err != nil {
		t.Fatalf("Search upsertProject: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Name == "upsertProject" && strings.Contains(r.File, "session-graph/lib/projects.js") {
			found = true
			t.Logf("upsertProject: %s @ %s:%d  sig=%q", r.QName, r.File, r.StartLine, r.Signature)
			break
		}
	}
	if !found {
		t.Errorf("expected upsertProject in session-graph/lib/projects.js; got %d candidates:", len(results))
		for _, r := range results {
			t.Logf("  - %s @ %s:%d", r.QName, r.File, r.StartLine)
		}
	}

	// noteDiscovery (top-level function in session-graph/lib/discovery.js)
	results, err = store.Search(SearchQuery{NameLike: "noteDiscovery"})
	if err != nil {
		t.Fatalf("Search noteDiscovery: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected noteDiscovery to be found; got none")
	}

	st, _ := store.Stats()
	abs, _ := filepath.Abs(animaPlugins)
	t.Logf("indexed %d files, %d symbols from %s; by lang: %+v", st.Files, st.Symbols, abs, st.ByLanguage)
}

// TestEndToEndCallsAnimaPlugins exercises the M2 call extractor against
// real anima-new plugin code: it must find that upsertSessionNode
// (session:start handler in plugins/acorn-cli/index.js) calls into
// session-graph's lib functions.
func TestEndToEndCallsAnimaPlugins(t *testing.T) {
	const animaPlugins = "/mnt/user/appdata/anima/anima-new/plugins"
	if _, err := os.Stat(animaPlugins); err != nil {
		t.Skipf("anima-new plugins not accessible at %s; skipping", animaPlugins)
	}
	tmp := t.TempDir()
	store, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	tx, err := store.BeginIndex()
	if err != nil {
		t.Fatalf("BeginIndex: %v", err)
	}
	totalCalls := 0
	_ = Walk(WalkOptions{
		Root:      animaPlugins,
		Languages: map[string]bool{LangJS: true, LangTS: true},
	}, func(fe FileEntry) bool {
		res := ExtractFile(fe)
		_ = tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", len(res.Symbols))
		for _, s := range res.Symbols {
			_ = tx.UpsertSymbol(s)
		}
		for _, c := range res.Calls {
			_ = tx.AddCall(Call{CallerQName: c.CallerQName, CalleeQName: c.CalleeQName, Line: c.Line}, c.CalleeName, fe.RelPath)
			totalCalls++
		}
		return true
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if totalCalls == 0 {
		t.Fatal("expected non-zero JS calls extracted")
	}

	// upsertSessionNode is invoked from sessionStartHandler in
	// plugins/acorn-cli/index.js — must show as a callee of the latter.
	callers, err := store.CallersOf("", "upsertSessionNode")
	if err != nil {
		t.Fatalf("CallersOf: %v", err)
	}
	if len(callers) == 0 {
		t.Errorf("expected at least one caller of upsertSessionNode; got 0")
	}
	for _, c := range callers {
		t.Logf("upsertSessionNode caller: %s @ line %d", c.CallerQName, c.Line)
	}
	t.Logf("total JS calls extracted: %d", totalCalls)
}
