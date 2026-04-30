package codeindex

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestWalkerSkipsNoiseDirs builds a synthetic project layout with files
// inside several noise dirs (node_modules, .venv, dist, vendor, etc.)
// and asserts the walker only emits the legitimate source files. This
// is the regression test for the "stuck walking node_modules" report —
// if anything ever broke the SkipDir branch, this would catch it.
func TestWalkerSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	// Files we expect to be emitted.
	want := []string{
		"main.go",
		"src/foo.ts",
		"src/sub/bar.js",
		"scripts/util.py",
		"crates/x/src/lib.rs",
	}
	// Files that MUST be skipped (in noise dirs or hidden dirs).
	skip := []string{
		"node_modules/lodash/index.js",
		"node_modules/react/dist/react.js",
		".venv/lib/python3.11/site-packages/foo/bar.py",
		"venv/bin/activate.py",
		"dist/bundle.js",
		"build/main.go",
		"target/debug/foo.rs",
		"out/x.js",
		"Pods/AFNetworking/foo.swift", // not a recognized ext anyway, but must be skipped
		"vendor/github.com/foo/bar.go",
		".git/hooks/pre-commit.py",
		".pnpm-store/v3/foo.js",
		"bower_components/jquery/jquery.js",
		".turbo/cache/foo.ts",
		".spore-code/index.db",
		".gradle/caches/x.go",
	}

	for _, p := range append(want, skip...) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var seen []string
	err := Walk(WalkOptions{Root: root}, func(fe FileEntry) bool {
		seen = append(seen, fe.RelPath)
		return true
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	sort.Strings(seen)
	sort.Strings(want)

	if len(seen) != len(want) {
		t.Errorf("walker emitted %d files, want %d", len(seen), len(want))
	}

	wantSet := map[string]bool{}
	for _, p := range want {
		wantSet[p] = true
	}
	for _, p := range seen {
		if !wantSet[p] {
			t.Errorf("unexpected emitted file: %q (should have been skipped)", p)
		}
	}
	for _, p := range want {
		found := false
		for _, s := range seen {
			if s == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected file not emitted: %q", p)
		}
	}

	// Extra: confirm no skipped path made it into seen via some
	// substring leak. Catches accidents like emitting "vendor/x.go"
	// when we meant to skip everything under "vendor/".
	for _, sp := range skip {
		for _, s := range seen {
			if s == sp {
				t.Errorf("walker emitted noise-dir file %q (should be skipped)", sp)
			}
		}
	}

	t.Logf("walker emitted %d/%d expected files", len(seen), len(want))
	for _, s := range seen {
		t.Logf("  %s", s)
	}
}

// TestWalkerHonorsSporeignore confirms .sporeignore overrides emit
// the listed paths.
func TestWalkerHonorsSporeignore(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"main.go", "secret.go"} {
		_ = os.WriteFile(filepath.Join(root, p), []byte("package x\n"), 0o644)
	}
	if err := os.WriteFile(filepath.Join(root, ".sporeignore"), []byte("secret.go\n# comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []string
	_ = Walk(WalkOptions{Root: root}, func(fe FileEntry) bool {
		seen = append(seen, fe.RelPath)
		return true
	})
	sort.Strings(seen)
	if len(seen) != 1 || seen[0] != "main.go" {
		t.Errorf("expected only main.go (secret.go in .sporeignore), got %v", seen)
	}
}
