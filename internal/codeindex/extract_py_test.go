package codeindex

import (
	"strings"
	"testing"
)

// TestExtractPyInline runs the Python extractor over a synthetic file
// covering the common shapes: top-level functions, classes with
// methods, async def, decorators, module constants, imports, and call
// edges. The test is anchored on shape rather than a real repo.
func TestExtractPyInline(t *testing.T) {
	src := []byte(`"""Module docstring.
Spans multiple lines.
"""
from os import path as p
import json
import collections.abc

API_KEY = "abc"
DEFAULT_TIMEOUT: int = 30
_private_const = 1

def greet(name: str) -> str:
    """Single-line docstring."""
    return "hi " + name

async def fetch(url: str) -> dict:
    return {}

def _hidden():
    return 42

class Repo:
    """A class docstring."""

    def __init__(self, name):
        self.name = name
        self._secret = "x"

    @staticmethod
    def make_default():
        return Repo("default")

    async def list_items(self):
        items = []
        for i in range(10):
            items.append(self._fetch_one(i))
        return items

    def _fetch_one(self, i):
        return self._secret + str(i)


class Subscription(Repo):
    def __init__(self, name, owner):
        super().__init__(name)
        self.owner = owner


def use_repo():
    r = Repo("foo")
    print(r.make_default())
    fetch("http://example.com")
`)

	res := pyExtractor{}.Extract(FileEntry{
		AbsPath:  "/synth/sample.py",
		RelPath:  "sample.py",
		Language: LangPython,
	}, src)
	if res.Err != nil {
		t.Fatalf("extract: %v", res.Err)
	}

	// Build a name → kind map for assertions.
	got := map[string]string{}
	gotContainer := map[string]string{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Kind
		gotContainer[s.Name] = s.Container
	}
	t.Logf("extracted %d symbols, %d calls, %d imports", len(res.Symbols), len(res.Calls), len(res.Imports))
	for _, s := range res.Symbols {
		t.Logf("  %s @ %d  kind=%s  container=%q  exp=%v  sig=%q", s.Name, s.StartLine, s.Kind, s.Container, s.Exported, s.Signature)
	}

	want := map[string]string{
		"API_KEY":         "const",
		"DEFAULT_TIMEOUT": "const",
		"greet":           "function",
		"fetch":           "function",
		"_hidden":         "function",
		"Repo":            "class",
		"__init__":        "method",
		"make_default":    "method",
		"list_items":      "method",
		"_fetch_one":      "method",
		"Subscription":    "class",
		"use_repo":        "function",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("symbol %q: want kind=%q, got kind=%q (present=%v)", name, kind, got[name], got[name] != "")
		}
	}

	// Method container should be the immediate enclosing class.
	for _, name := range []string{"__init__", "make_default", "list_items", "_fetch_one"} {
		// __init__ appears in both Repo AND Subscription. The map
		// overwrites with the last container seen — accept either.
		if c := gotContainer[name]; c != "Repo" && c != "Subscription" {
			t.Errorf("method %q container expected Repo or Subscription, got %q", name, c)
		}
	}

	// _private_const should NOT be captured (lowercase = not SCREAMING_CASE).
	if _, present := got["_private_const"]; present {
		t.Errorf("_private_const should not be captured (lowercase name)")
	}

	// Exported flag: leading underscore = not exported.
	for _, s := range res.Symbols {
		if s.Name == "_hidden" && s.Exported {
			t.Errorf("_hidden marked exported, want not exported")
		}
		if s.Name == "Repo" && !s.Exported {
			t.Errorf("Repo not marked exported")
		}
	}

	// Calls — use_repo() calls Repo, fetch (filtered: super, print
	// would be filtered as builtin).
	useRepoCalls := map[string]int{}
	for _, c := range res.Calls {
		if strings.HasSuffix(c.CallerQName, "::use_repo") {
			useRepoCalls[c.CalleeName]++
		}
	}
	for _, want := range []string{"Repo", "fetch"} {
		if useRepoCalls[want] == 0 {
			t.Errorf("use_repo should have a call edge to %q (got %v)", want, useRepoCalls)
		}
	}
	if useRepoCalls["print"] > 0 {
		t.Errorf("use_repo should NOT have a call edge to print (filtered as builtin)")
	}

	// list_items inside Repo should call self._fetch_one — appears as
	// callee_name=_fetch_one (the dot prefix is matched but the
	// receiver isn't part of the captured name).
	listItemsCalls := map[string]int{}
	for _, c := range res.Calls {
		if strings.HasSuffix(c.CallerQName, "::Repo._fetch_one") || strings.HasSuffix(c.CallerQName, "::Repo.list_items") {
			listItemsCalls[c.CalleeName]++
		}
	}
	if listItemsCalls["_fetch_one"] == 0 {
		t.Errorf("list_items should have a call edge to _fetch_one (got %v)", listItemsCalls)
	}

	// Imports: from os import path → target os; import json → target json.
	importTargets := map[string]bool{}
	for _, im := range res.Imports {
		importTargets[im.Target] = true
	}
	for _, want := range []string{"os", "json"} {
		if !importTargets[want] {
			t.Errorf("expected import target %q; got %v", want, importTargets)
		}
	}
}
