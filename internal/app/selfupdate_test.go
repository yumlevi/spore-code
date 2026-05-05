package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLocalUpdateSourceExplicitDir(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, currentAssetName())
	if err := os.WriteFile(want, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
		t.Fatal(err)
	}

	got, label, err := resolveLocalUpdateSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("source path = %q, want %q", got, want)
	}
	if label == "" {
		t.Fatalf("expected non-empty version label")
	}
}

func TestLocalUpdateCandidateMissingPlatformAsset(t *testing.T) {
	dir := t.TempDir()
	if _, ok := localUpdateCandidate(dir); ok {
		t.Fatalf("empty dir should not resolve as a local update")
	}
}
