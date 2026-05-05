package app

import (
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.6", "v1.0.5", 1},
		{"v1.0.6", "v1.0.6", 0},
		{"v1.0.6", "v1.0.7", -1},
		{"v1.0.6", "v1.0.6-dirty", 0},
		{"0.11.0", "v1.0.6", -1},
	}
	for _, tc := range cases {
		got, ok := compareSemver(tc.a, tc.b)
		if !ok || got != tc.want {
			t.Fatalf("compareSemver(%q,%q)=%d,%v want %d,true", tc.a, tc.b, got, ok, tc.want)
		}
	}
}

func TestUpdateCheckMessageLocalBuild(t *testing.T) {
	msg := updateCheckMessage("v1.0.6-dirty", "v1.0.6", "https://example.invalid")
	if !strings.Contains(msg, "No newer published stable release") {
		t.Fatalf("expected local-build explanation, got %q", msg)
	}
	if !strings.Contains(msg, "Current build: v1.0.6-dirty") || !strings.Contains(msg, "Latest published: v1.0.6") {
		t.Fatalf("expected current/latest details, got %q", msg)
	}
}

func TestUpdateCheckMessageUpdateAvailable(t *testing.T) {
	msg := updateCheckMessage("v0.11.0", "v1.0.6", "https://example.invalid")
	if !strings.Contains(msg, "Update available: v0.11.0 → v1.0.6") {
		t.Fatalf("expected update-available message, got %q", msg)
	}
}
