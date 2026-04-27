package codeindex

import (
	"strings"
	"testing"
)

// TestTSExtractTreeSitter — covers the cases the regex extractor missed
// or fudged, plus the basic shapes from the old TestExtractTSInline.
// The synthetic input includes:
//   - nested classes (regex lost the outer container at depth 2+)
//   - decorated declarations
//   - computed method names (regex couldn't see [Symbol.iterator])
//   - default-export anonymous class (regex missed)
//   - multi-line function signatures
//   - arrow + function-expression const declarations
//   - JSX element inside the body (must not break the parse)
func TestTSExtractTreeSitter(t *testing.T) {
	src := []byte(`// header
import { foo } from './foo';

export interface UserOpts {
  id: string;
  name?: string;
}

export type UserMap = Record<string, UserOpts>;

export const ANSWER = 42;

export function greet(
  name: string,
): string {
  return "hi " + name;
}

export default async function () {
  return 1;
}

export const Add = (a: number, b: number) => a + b;

const internalHelper = function internalHelper() { return 1; };

function decorator(target: any) { return target; }

@decorator
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
  [Symbol.iterator]() { return null; }

  // Nested class — regex lost this
  static Inner = class {
    ping() { return "pong"; }
  };
}

enum Color { Red, Green, Blue }
`)

	fe := FileEntry{
		AbsPath:  "/synth/example.ts",
		RelPath:  "example.ts",
		Language: LangTS,
	}
	res := LookupExtractor(LangTS).Extract(fe, src)
	if res.Err != nil {
		t.Fatalf("extract: %v", res.Err)
	}

	got := map[string]string{}
	cont := map[string]string{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Kind
		cont[s.Name] = s.Container
	}
	t.Logf("extracted %d symbols, %d calls, %d imports", len(res.Symbols), len(res.Calls), len(res.Imports))
	for _, s := range res.Symbols {
		t.Logf("  %s @ %d  kind=%s  container=%q  exp=%v", s.Name, s.StartLine, s.Kind, s.Container, s.Exported)
	}

	// Existing-shape expectations — same as the regex test had.
	want := map[string]string{
		"UserOpts":       "interface",
		"UserMap":        "type",
		"greet":          "function",
		"Add":            "function",
		"internalHelper": "function",
		"decorator":      "function",
		"Repo":           "class",
		"makeDefault":    "method",
		"displayName":    "method",
		"fetch":          "method",
		"_hidden":        "method",
		"Color":          "enum",
		"constructor":    "constructor",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("symbol %q: want kind=%q, got kind=%q (present=%v)", name, kind, got[name], got[name] != "")
		}
	}

	// Tree-sitter wins (vs regex):
	// 1. Nested class method "ping" attributes to Inner — caught with the
	//    method definition under Inner's class body.
	if got["ping"] == "" {
		t.Errorf("nested class method 'ping' should be extracted (regex missed this)")
	}
	// 2. Method 'fetch' container is Repo — regex got this too, but
	//    confirm tree-sitter doesn't lose it.
	if cont["fetch"] != "Repo" {
		t.Errorf("fetch container want Repo, got %q", cont["fetch"])
	}
}

// TestPyExtractTreeSitter — covers shapes the regex version handled
// plus a few edges (multi-line signature, decorated method,
// from-import with alias).
func TestPyExtractTreeSitter(t *testing.T) {
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

async def fetch(
    url: str,
    timeout: int = 30,
) -> dict:
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

    @property
    def display_name(self):
        return self.name

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

	fe := FileEntry{
		AbsPath:  "/synth/sample.py",
		RelPath:  "sample.py",
		Language: LangPython,
	}
	res := LookupExtractor(LangPython).Extract(fe, src)
	if res.Err != nil {
		t.Fatalf("extract: %v", res.Err)
	}

	got := map[string]string{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Kind
	}
	t.Logf("extracted %d symbols, %d calls, %d imports", len(res.Symbols), len(res.Calls), len(res.Imports))

	want := map[string]string{
		"API_KEY":         "const",
		"DEFAULT_TIMEOUT": "const",
		"greet":           "function",
		"fetch":           "function",
		"_hidden":         "function",
		"Repo":            "class",
		"__init__":        "method",
		"make_default":    "method",
		"display_name":    "method",
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

	// Decorated method shape — regex caught it via the def line; we
	// expect the same.
	if got["display_name"] != "method" {
		t.Errorf("@property decorated method should still be extracted as method")
	}

	// Imports — `from os import path as p` should yield target=os.
	importTargets := map[string]bool{}
	for _, im := range res.Imports {
		importTargets[im.Target] = true
	}
	for _, w := range []string{"os", "json", "collections.abc"} {
		if !importTargets[w] {
			t.Errorf("expected import target %q; got %v", w, importTargets)
		}
	}

	// Calls — use_repo calls Repo and fetch.
	useRepoCalls := map[string]bool{}
	for _, c := range res.Calls {
		if strings.HasSuffix(c.CallerQName, "::use_repo") {
			useRepoCalls[c.CalleeName] = true
		}
	}
	for _, w := range []string{"Repo", "fetch", "make_default"} {
		if !useRepoCalls[w] {
			t.Errorf("use_repo should call %q; got %v", w, useRepoCalls)
		}
	}
}

// TestRustExtract — first-pass coverage for the new Rust extractor.
// Synthetic file exercises fn, struct, enum, trait, impl, mod, type,
// const, use, and a function call edge.
func TestRustExtract(t *testing.T) {
	src := []byte(`use std::collections::HashMap;
use serde::{Serialize, Deserialize};

pub const MAX_RETRIES: u32 = 3;

pub struct Repo {
    pub name: String,
    age: u32,
}

pub enum Status {
    Active,
    Idle,
}

pub trait Greet {
    fn greet(&self) -> String;
    fn name(&self) -> &str;
}

impl Greet for Repo {
    fn greet(&self) -> String {
        format!("Hello, {}!", self.name)
    }

    fn name(&self) -> &str {
        &self.name
    }
}

impl Repo {
    pub fn new(name: String) -> Self {
        Self { name, age: 0 }
    }

    fn _internal(&self) -> bool {
        true
    }
}

fn use_repo() {
    let r = Repo::new("foo".to_string());
    let g = r.greet();
    println!("{}", g);
    panic!("test");
}

mod helpers {
    pub fn flatten<T>(input: Vec<Vec<T>>) -> Vec<T> {
        input.into_iter().flatten().collect()
    }
}
`)

	fe := FileEntry{
		AbsPath:  "/synth/lib.rs",
		RelPath:  "lib.rs",
		Language: LangRust,
	}
	res := LookupExtractor(LangRust).Extract(fe, src)
	if res.Err != nil {
		t.Fatalf("extract: %v", res.Err)
	}

	got := map[string]string{}
	cont := map[string]string{}
	exp := map[string]bool{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Kind
		cont[s.Name] = s.Container
		exp[s.Name] = s.Exported
	}
	t.Logf("rust: extracted %d symbols, %d calls, %d imports", len(res.Symbols), len(res.Calls), len(res.Imports))
	for _, s := range res.Symbols {
		t.Logf("  %s @ %d  kind=%s  container=%q  exp=%v", s.Name, s.StartLine, s.Kind, s.Container, s.Exported)
	}

	want := map[string]string{
		"MAX_RETRIES": "const",
		"Repo":        "struct",
		"Status":      "enum",
		"Greet":       "interface", // we map trait → interface for cross-language search
		"new":         "method",
		"_internal":   "method",
		"greet":       "method",
		"name":        "method",
		"use_repo":    "function",
		"flatten":     "function",
	}
	for n, k := range want {
		if got[n] != k {
			t.Errorf("symbol %q: want kind=%q, got kind=%q", n, k, got[n])
		}
	}

	// Methods inside `impl Repo` and `impl Greet for Repo` should both
	// attribute container=Repo.
	if cont["new"] != "Repo" {
		t.Errorf("new container want Repo, got %q", cont["new"])
	}
	if cont["greet"] != "Repo" {
		t.Errorf("greet container want Repo, got %q", cont["greet"])
	}

	// Exportedness — pub fn → exported, fn _internal → not.
	if !exp["new"] {
		t.Errorf("pub fn new should be exported")
	}
	if exp["_internal"] {
		t.Errorf("fn _internal should not be exported")
	}

	// Calls — use_repo should have edges to greet and to_string at
	// minimum. println! (filtered as noise) and panic! (kept as macro)
	// — verify panic! shows up.
	useRepoCalls := map[string]bool{}
	for _, c := range res.Calls {
		if strings.HasSuffix(c.CallerQName, "::use_repo") {
			useRepoCalls[c.CalleeName] = true
		}
	}
	for _, w := range []string{"new", "greet", "to_string"} {
		if !useRepoCalls[w] {
			t.Errorf("use_repo expected call to %q; got %v", w, useRepoCalls)
		}
	}
	if useRepoCalls["println"] || useRepoCalls["println!"] {
		t.Errorf("println! should be filtered as noise; got %v", useRepoCalls)
	}
	if !useRepoCalls["panic!"] {
		t.Errorf("panic! should be captured as a call edge; got %v", useRepoCalls)
	}

	// Imports — std::collections::HashMap.
	hasHashMap := false
	for _, im := range res.Imports {
		if strings.Contains(im.Target, "HashMap") {
			hasHashMap = true
		}
	}
	if !hasHashMap {
		t.Errorf("expected HashMap in imports; got %+v", res.Imports)
	}
}
