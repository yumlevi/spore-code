package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Version is the running binary's release tag — set by main.go from
// the `var version` constant (which is overrideable via -ldflags
// "-X main.version=vX.Y.Z" so CI can stamp it). Compared against the
// GitHub `latest` tag to tell the user whether they're up to date.
var Version = "dev"

// SetVersion is the wiring hook for cmd/acorn/main.go.
func SetVersion(v string) { Version = v }

// versionLE returns true when 'a' is older than or equal to 'b' under
// loose semver: 'v1.2.3' parts compared numerically, missing parts as
// zero, leading 'v' optional. Returns false if either string is
// unparseable so we lean toward 'show update available' rather than
// silently skip.
func versionLE(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return true
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.SplitN(s, "-", 2)[0] // drop pre-release suffix
	xs := strings.Split(parts, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(xs); i++ {
		n := 0
		for _, c := range xs[i] {
			if c < '0' || c > '9' {
				return out, false
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out, true
}

// updateCheckResult carries GitHub release info back to the UI.
type updateCheckResult struct {
	OK      bool
	Version string
	URL     string
	Err     string
}

// bootUpdateMsg is the silent variant — emitted by Init() and handled
// only when an update is actually available. Up-to-date and any error
// (no network on the user's box, GitHub rate-limit, etc.) are dropped
// without spamming the chat. Don't reuse updateCheckResult here so the
// /update check handler can stay loud for explicit user requests.
type bootUpdateMsg struct {
	Version string // empty if no update available or check failed
	URL     string
}

func (r updateCheckResult) teaMsg() tea.Msg { return r }

// bootCheckUpdateCmd pings GitHub once at startup. Returns bootUpdateMsg
// with Version set ONLY when a newer release exists; up-to-date and any
// network/HTTP error return an empty msg so the handler stays quiet.
//
// Wired into Init() in model.go alongside dial/recv/tool cmds. 8s
// timeout so a slow GitHub doesn't delay the chat — the result lands
// asynchronously whenever it returns.
func bootCheckUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 8 * time.Second}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases/latest", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return bootUpdateMsg{}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return bootUpdateMsg{}
		}
		var rel struct {
			TagName string `json:"tag_name"`
			URL     string `json:"html_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return bootUpdateMsg{}
		}
		// Only surface when latest is strictly newer than the running
		// version. versionLE returns true when 'a <= b'; we want a < b.
		if rel.TagName == "" || versionLE(rel.TagName, Version) {
			return bootUpdateMsg{}
		}
		return bootUpdateMsg{Version: rel.TagName, URL: rel.URL}
	}
}

// releaseInfo is one entry returned by the GitHub /releases endpoint
// — the subset we render in /update list and use to resolve fuzzy
// installs (`/update install graphcorn`, `/update install pre`).
type releaseInfo struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	URL        string `json:"html_url"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
	PublishedAt string `json:"published_at"`
}

// releaseListResult — Tea message carrying a slice of releases for the
// /update list handler to render. Capped at 25 so the chat bubble
// doesn't explode on long release histories.
type releaseListResult struct {
	Err      string
	Releases []releaseInfo
}

// fetchAllReleasesCmd hits GitHub's /releases endpoint (NOT /releases/latest
// which silently skips pre-releases). Returns up to 25 most recent
// releases for the /update list handler to display. Pre-releases like
// v0.2.0-graphcorn show up here even though they don't show up in the
// stable channel that the boot-time check uses.
func fetchAllReleasesCmd() tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases?per_page=25", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return releaseListResult{Err: err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return releaseListResult{Err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		var releases []releaseInfo
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return releaseListResult{Err: err.Error()}
		}
		// Defend against GitHub's /releases list endpoint silently
		// omitting valid releases: if the running version isn't in the
		// list, probe the direct tag endpoint and prepend it. Same
		// quirk we hit with v0.9.0-pre-verify — the release exists at
		// /releases/tags/<tag> but is missing from the list.
		if Version != "" && Version != "dev" {
			seen := false
			for _, r := range releases {
				if r.TagName == Version {
					seen = true
					break
				}
			}
			if !seen {
				if rel, ok := fetchTagDirect(Version); ok {
					releases = append([]releaseInfo{rel}, releases...)
				}
			}
		}
		return releaseListResult{Releases: releases}
	}
}

// installResolveResult — same shape as updateInstallResult but tagged
// distinctly so the handler can show the resolution explanation
// (e.g. "Resolved 'graphcorn' → v0.2.0-graphcorn") before / instead
// of the install. Currently the standard install path is what runs;
// this type exists for future use if we want pre-install confirm.
//
// resolveAndInstallCmd takes a user-supplied query (a literal tag, a
// keyword like 'pre' or 'stable', or a substring like 'graphcorn'),
// fetches the release list, picks the best match, and dispatches the
// install. Single round-trip from user typing to binary swap.
func resolveAndInstallCmd(query string) tea.Cmd {
	return func() tea.Msg {
		query = strings.TrimSpace(query)
		// Empty / "stable" / "latest" → fall through to the standard
		// /releases/latest path (skips pre-releases).
		if query == "" || query == "stable" || query == "latest" {
			return installUpdateCmd("")()
		}

		// Need the full list to pick from for everything else.
		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases?per_page=50", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return updateInstallResult{Err: "release list fetch failed: " + err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return updateInstallResult{Err: fmt.Sprintf("release list HTTP %d", resp.StatusCode)}
		}
		var rels []releaseInfo
		if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
			return updateInstallResult{Err: "release list decode: " + err.Error()}
		}

		// Resolution rules, in order:
		//   1. exact tag match wins (e.g. "v0.2.0-graphcorn")
		//   2. keyword "pre" / "prerelease" → first prerelease in the list
		//   3. substring match against tag name → newest matching tag
		// All matches skip drafts.
		filter := func(predicate func(releaseInfo) bool) *releaseInfo {
			for i := range rels {
				if rels[i].Draft { continue }
				if predicate(rels[i]) { return &rels[i] }
			}
			return nil
		}
		var picked *releaseInfo
		// Exact tag
		picked = filter(func(r releaseInfo) bool { return r.TagName == query || r.TagName == "v"+query })
		// Keywords
		if picked == nil && (query == "pre" || query == "prerelease" || query == "experimental") {
			picked = filter(func(r releaseInfo) bool { return r.Prerelease })
		}
		// Substring (longest-tag wins via list order — releases are newest first)
		if picked == nil {
			ql := strings.ToLower(query)
			picked = filter(func(r releaseInfo) bool { return strings.Contains(strings.ToLower(r.TagName), ql) || strings.Contains(strings.ToLower(r.Name), ql) })
		}
		if picked == nil {
			// Fall back to the direct tag endpoint. The GitHub /releases
			// list endpoint occasionally omits valid published releases
			// (we hit this with v0.9.0-pre-verify in 04/26 — release was
			// public, all assets uploaded, but missing from the list).
			// Probe `/releases/tags/<query>` and `/releases/tags/v<query>`
			// before giving up so users aren't blocked by GitHub flakiness.
			if rel, ok := fetchTagDirect(query); ok {
				return installUpdateCmd(rel.TagName)()
			}
			if rel, ok := fetchTagDirect("v" + query); ok {
				return installUpdateCmd(rel.TagName)()
			}
			return updateInstallResult{Err: fmt.Sprintf("no release matches %q (try /update list)", query)}
		}
		return installUpdateCmd(picked.TagName)()
	}
}

// fetchTagDirect hits /releases/tags/<tag> as a fallback path when the
// /releases list endpoint omits a release. Returns (rel, true) only on
// HTTP 200 + non-draft.
func fetchTagDirect(tag string) (releaseInfo, bool) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases/tags/"+tag, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return releaseInfo{}, false
	}
	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return releaseInfo{}, false
	}
	if rel.Draft || rel.TagName == "" {
		return releaseInfo{}, false
	}
	return rel, true
}

// checkUpdateCmd pings GitHub for the latest release tag.
func checkUpdateCmd(checkOnly bool) tea.Cmd {
	_ = checkOnly // no distinction for now — we never install in-process.
	return func() tea.Msg {
		client := &http.Client{Timeout: 8 * time.Second}
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases/latest", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := client.Do(req)
		if err != nil {
			return updateCheckResult{Err: err.Error()}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return updateCheckResult{Err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		var rel struct {
			TagName string `json:"tag_name"`
			URL     string `json:"html_url"`
		}
		if err := json.Unmarshal(body, &rel); err != nil {
			return updateCheckResult{Err: err.Error()}
		}
		return updateCheckResult{OK: true, Version: rel.TagName, URL: rel.URL}
	}
}
