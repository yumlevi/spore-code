package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateInstallResult is returned from the self-install command.
type updateInstallResult struct {
	OK      bool
	Version string
	Path    string
	Err     string
}

// installUpdateCmd downloads the latest release binary matching the current
// GOOS+GOARCH and atomically replaces the running acorn executable.
//
// Implementation notes:
// - Downloads to a sibling temp file in the install dir so os.Rename works
//   atomically (same filesystem).
// - On Windows, we can't rename-over a running exe, so we rename the old
//   one aside first. The user has to re-launch for the new version.
// - Validates the download is non-empty and has ELF/PE magic before swap.
func installUpdateCmd(version string) tea.Cmd {
	return func() tea.Msg {
		exePath, err := os.Executable()
		if err != nil {
			return updateInstallResult{Err: "cannot locate current binary: " + err.Error()}
		}
		exePath, _ = filepath.EvalSymlinks(exePath)

		// Resolve asset URL from the release tag.
		tag := version
		if tag == "" {
			t, u, err := fetchLatestTag()
			if err != nil {
				return updateInstallResult{Err: err.Error()}
			}
			tag, _ = t, u
		}
		url := fmt.Sprintf(
			"https://github.com/yumlevi/acorn-cli/releases/download/%s/acorn-%s-%s",
			tag, runtime.GOOS, runtime.GOARCH,
		)
		if runtime.GOOS == "windows" {
			url += ".exe"
		}

		// Download.
		client := &http.Client{Timeout: 2 * time.Minute}
		resp, err := client.Get(url)
		if err != nil {
			return updateInstallResult{Err: "download failed: " + err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return updateInstallResult{Err: fmt.Sprintf("download HTTP %d at %s", resp.StatusCode, url)}
		}
		dir := filepath.Dir(exePath)
		tmp, err := os.CreateTemp(dir, ".acorn-update-*")
		if err != nil {
			return updateInstallResult{Err: "cannot create temp file in " + dir + ": " + err.Error()}
		}
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return updateInstallResult{Err: "write failed: " + err.Error()}
		}
		_ = tmp.Close()

		// Magic-byte check: the download must be ELF or PE, not HTML 404.
		if !looksLikeBinary(tmp.Name()) {
			_ = os.Remove(tmp.Name())
			return updateInstallResult{Err: "downloaded file doesn't look like a binary (probably 404'd or the release asset is missing for this platform)"}
		}
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			_ = os.Remove(tmp.Name())
			return updateInstallResult{Err: "chmod failed: " + err.Error()}
		}

		// Swap. On Windows we move the running exe aside first.
		if runtime.GOOS == "windows" {
			old := exePath + ".old"
			_ = os.Remove(old)
			if err := os.Rename(exePath, old); err != nil {
				_ = os.Remove(tmp.Name())
				return updateInstallResult{Err: "cannot rename running exe aside: " + err.Error()}
			}
		}
		if err := os.Rename(tmp.Name(), exePath); err != nil {
			_ = os.Remove(tmp.Name())
			return updateInstallResult{Err: "atomic rename failed: " + err.Error()}
		}
		return updateInstallResult{OK: true, Version: tag, Path: exePath}
	}
}

func fetchLatestTag() (string, string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/acorn-cli/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		URL     string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}
	if rel.TagName == "" {
		return "", "", errors.New("no tag_name in release response")
	}
	return rel.TagName, rel.URL, nil
}

func looksLikeBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, _ := f.Read(buf)
	if n < 4 {
		return false
	}
	// ELF: 0x7F 'E' 'L' 'F'   PE/COFF (MZ header): 0x4D 0x5A ('MZ')
	// Mach-O 64-bit: 0xCF FA ED FE  (little-endian)  or 0xFE ED FA CF (big-endian)
	b := buf[:4]
	if b[0] == 0x7F && b[1] == 'E' && b[2] == 'L' && b[3] == 'F' {
		return true
	}
	if b[0] == 'M' && b[1] == 'Z' {
		return true
	}
	if (b[0] == 0xCF && b[1] == 0xFA) || (b[3] == 0xCF && b[2] == 0xFA) {
		return true
	}
	return false
}
