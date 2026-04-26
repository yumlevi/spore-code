package codeindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArchitectureAcornCli runs the architecture summary against the
// indexed acorn-cli repo. Validates: clusters around the obvious
// top-level dirs (cmd, internal), Go-dominant tech stack, main entry
// point present, calls=0 in M1.
func TestArchitectureAcornCli(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		root = filepath.Dir(root)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skip("repo root with go.mod not found")
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
	_ = Walk(WalkOptions{Root: root, Languages: map[string]bool{LangGo: true}}, func(fe FileEntry) bool {
		res := ExtractFile(fe)
		_ = tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", len(res.Symbols))
		for _, sym := range res.Symbols {
			_ = tx.UpsertSymbol(sym)
		}
		for _, c := range res.Calls {
			_ = tx.AddCall(Call{CallerQName: c.CallerQName, CalleeQName: c.CalleeQName, Line: c.Line}, c.CalleeName, fe.RelPath)
		}
		return true
	})
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	arch, err := ComputeArchitecture(store)
	if err != nil {
		t.Fatalf("ComputeArchitecture: %v", err)
	}

	if arch.Stats.Files == 0 || arch.Stats.Symbols == 0 {
		t.Fatalf("expected non-zero stats; got %+v", arch.Stats)
	}
	if arch.Stats.Calls == 0 {
		t.Errorf("expected non-zero CALLS edges after M2, got %d", arch.Stats.Calls)
	}

	// Tech stack: Go must lead.
	if len(arch.TechStack) == 0 {
		t.Fatal("empty tech stack")
	}
	if arch.TechStack[0].Language != LangGo {
		t.Errorf("expected Go to lead tech stack, got %s", arch.TechStack[0].Language)
	}

	// Clusters: should include cmd and internal at minimum.
	gotClusters := map[string]int{}
	for _, c := range arch.Clusters {
		gotClusters[c.Name] = c.Files
	}
	for _, want := range []string{"internal", "cmd"} {
		if gotClusters[want] == 0 {
			t.Errorf("expected cluster %q with files > 0; got %v", want, gotClusters)
		}
	}

	// Entry point: main package's main func.
	hasMain := false
	for _, ep := range arch.EntryPoints {
		if ep.Name == "main" && ep.Kind == "main" && strings.HasSuffix(ep.File, "main.go") {
			hasMain = true
			break
		}
	}
	if !hasMain {
		t.Errorf("expected a main() entry point; got %d entries: %+v", len(arch.EntryPoints), arch.EntryPoints)
	}

	// Hot paths: populated after M2 added Go CALLS extraction.
	if len(arch.HotPaths) == 0 {
		t.Errorf("expected non-empty hot paths after M2; got 0")
	}
	for _, hp := range arch.HotPaths {
		t.Logf("  hot %s  callers=%d  file=%s:%d", hp.QName, hp.Callers, hp.File, hp.Line)
	}

	t.Logf("architecture summary: %d files, %d symbols (%d funcs, %d methods, %d classes), %d clusters, %d entry points",
		arch.Stats.Files, arch.Stats.Symbols, arch.Stats.Functions, arch.Stats.Methods, arch.Stats.Classes,
		len(arch.Clusters), len(arch.EntryPoints))
	for _, c := range arch.Clusters {
		t.Logf("  cluster %s: %d files (%s)", c.Name, c.Files, c.DominantLang)
	}
	for _, ep := range arch.EntryPoints {
		t.Logf("  entry %s @ %s:%d (%s)", ep.QName, ep.File, ep.Line, ep.Kind)
	}
}
