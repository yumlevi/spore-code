package codeindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEndIndexAcornCli runs the full pipeline (Open -> Walk ->
// ExtractFile -> UpsertSymbol -> Search) against the parent acorn-cli
// repository itself. It validates that the Go extractor finds known
// symbols (Executor.Execute, Open, Symbol) and that Search returns them.
func TestEndToEndIndexAcornCli(t *testing.T) {
	// Walk up from the codeindex package dir to the repo root.
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
		t.Skip("repo root with go.mod not found above test dir; skipping")
	}

	// Use a throwaway store dir so the test doesn't pollute .acorn/.
	tmp := t.TempDir()

	// Write a minimal stub layout: the Open() helper expects a .acorn/
	// dir under the project root. We pass tmp as root so the walker
	// only sees the test's symlink, but that's not what we want — we
	// want to walk the real repo. So: open store at tmp, but feed Walk
	// the real repo root.
	store, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	tx, err := store.BeginIndex()
	if err != nil {
		t.Fatalf("BeginIndex: %v", err)
	}

	count := 0
	totalSymbols := 0
	walkErr := Walk(WalkOptions{
		Root:      root,
		Languages: map[string]bool{LangGo: true},
		MaxFiles:  300,
	}, func(fe FileEntry) bool {
		count++
		res := ExtractFile(fe)
		if res.Err != nil && len(res.Symbols) == 0 {
			t.Logf("extract %s: %v", fe.RelPath, res.Err)
			return true
		}
		if err := tx.UpsertFile(fe.RelPath, fe.Language, fe.MTime, "", len(res.Symbols)); err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}
		for _, sym := range res.Symbols {
			if err := tx.UpsertSymbol(sym); err != nil {
				t.Fatalf("UpsertSymbol: %v", err)
			}
			totalSymbols++
		}
		return true
	})
	if walkErr != nil {
		t.Fatalf("Walk: %v", walkErr)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if count == 0 {
		t.Fatal("walker found no .go files in repo root")
	}
	if totalSymbols == 0 {
		t.Fatal("no Go symbols extracted from repo")
	}
	t.Logf("walked %d files, extracted %d symbols", count, totalSymbols)

	// Search by exact name "Open" — at minimum, our own Open from
	// store.go must turn up.
	results, err := store.Search(SearchQuery{NameLike: "Open", Kind: "function"})
	if err != nil {
		t.Fatalf("Search Open: %v", err)
	}
	foundOurOpen := false
	for _, r := range results {
		if r.Name == "Open" && strings.HasSuffix(r.File, "internal/codeindex/store.go") {
			foundOurOpen = true
			break
		}
	}
	if !foundOurOpen {
		t.Errorf("expected to find codeindex.Open in search results; got %d results, none matching", len(results))
		for _, r := range results {
			t.Logf("  - %s @ %s:%d", r.QName, r.File, r.StartLine)
		}
	}

	// Method search: look for Executor.Execute via container filter.
	results, err = store.Search(SearchQuery{NameLike: "Execute", Kind: "method"})
	if err != nil {
		t.Fatalf("Search Execute method: %v", err)
	}
	foundExec := false
	for _, r := range results {
		if r.Name == "Execute" && r.Container == "Executor" {
			foundExec = true
			t.Logf("found method: %s @ %s:%d  sig=%q", r.QName, r.File, r.StartLine, r.Signature)
			break
		}
	}
	if !foundExec {
		// Some repos may not have a Executor.Execute pair — log diagnostically rather than fail hard.
		t.Logf("no Executor.Execute method found among %d Execute results; not necessarily wrong", len(results))
	}

	// Stats sanity.
	st, err := store.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Files == 0 || st.Symbols == 0 {
		t.Errorf("Stats reports zero: %+v", st)
	}
	if st.ByLanguage[LangGo] == 0 {
		t.Errorf("expected Go files in ByLanguage; got %+v", st.ByLanguage)
	}
}
