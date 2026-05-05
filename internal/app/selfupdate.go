package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
//   - Downloads to a sibling temp file in the install dir so os.Rename works
//     atomically (same filesystem).
//   - On Windows, the running .exe is locked. We stage a replacement script
//     that waits for Spore to exit, then swaps the binary.
//   - Validates the download is non-empty and has ELF/PE magic before swap.
func installUpdateCmd(version string) tea.Cmd {
	return func() tea.Msg {
		// Resolve asset URL from the release tag.
		tag := version
		if tag == "" {
			t, u, err := fetchLatestTag()
			if err != nil {
				return updateInstallResult{Err: err.Error()}
			}
			tag, _ = t, u
		}
		url := fmt.Sprintf("https://github.com/yumlevi/spore-code/releases/download/%s/%s", tag, currentAssetName())

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
		exePath, err := currentExecutablePath()
		if err != nil {
			return updateInstallResult{Err: err.Error()}
		}
		dir := filepath.Dir(exePath)
		tmp, err := os.CreateTemp(dir, ".spore-update-*")
		if err != nil {
			return updateInstallResult{Err: "cannot create temp file in " + dir + ": " + err.Error()}
		}
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return updateInstallResult{Err: "write failed: " + err.Error()}
		}
		_ = tmp.Close()

		return installTempFile(tmp.Name(), tag)
	}
}

// installLocalUpdateCmd installs a locally-built binary instead of downloading
// from GitHub. It searches:
//   - explicit file/dir passed after `/update install local`
//   - SPORE_CODE_UPDATE_BINARY
//   - SPORE_CODE_UPDATE_DIR
//   - ./dist and sibling dist/ beside the running executable
//   - ~/.spore-code/updates
func installLocalUpdateCmd(query string) tea.Cmd {
	return func() tea.Msg {
		src, label, err := resolveLocalUpdateSource(query)
		if err != nil {
			return updateInstallResult{Err: err.Error()}
		}
		return installSourceFile(src, label)
	}
}

func currentAssetName() string {
	name := fmt.Sprintf("spore-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func currentExecutablePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate current binary: %w", err)
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	return exePath, nil
}

func resolveLocalUpdateSource(query string) (string, string, error) {
	query = strings.TrimSpace(query)
	if query == "local" || query == "dev" {
		query = ""
	}
	if query != "" {
		if p, ok := localUpdateCandidate(query); ok {
			return p, readBinaryVersion(p), nil
		}
		return "", "", fmt.Errorf("local update source not found: %s", query)
	}

	if p := os.Getenv("SPORE_CODE_UPDATE_BINARY"); p != "" {
		if path, ok := localUpdateCandidate(p); ok {
			return path, readBinaryVersion(path), nil
		}
	}

	var dirs []string
	if d := os.Getenv("SPORE_CODE_UPDATE_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if exePath, err := currentExecutablePath(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exePath), "dist"))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, "dist"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".spore-code", "updates"))
	}

	for _, dir := range dirs {
		if p, ok := localUpdateCandidate(dir); ok {
			return p, readBinaryVersion(p), nil
		}
	}

	return "", "", fmt.Errorf(
		"no local %s binary found. Run scripts/build.sh or scripts/release.sh, or set SPORE_CODE_UPDATE_DIR / pass `/update install local /path/to/dist`",
		currentAssetName(),
	)
}

func localUpdateCandidate(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			candidate := filepath.Join(path, currentAssetName())
			if st2, err := os.Stat(candidate); err == nil && !st2.IsDir() {
				return candidate, true
			}
			return "", false
		}
		return path, true
	}
	return "", false
}

func readBinaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return filepath.Base(path)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return filepath.Base(path)
	}
	return fields[len(fields)-1]
}

func installSourceFile(srcPath, version string) updateInstallResult {
	exePath, err := currentExecutablePath()
	if err != nil {
		return updateInstallResult{Err: err.Error()}
	}
	if sameFile(srcPath, exePath) {
		return updateInstallResult{Err: "local update source is already the running executable"}
	}
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".spore-update-*")
	if err != nil {
		return updateInstallResult{Err: "cannot create temp file in " + dir + ": " + err.Error()}
	}
	src, err := os.Open(srcPath)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return updateInstallResult{Err: "cannot open local update source: " + err.Error()}
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return updateInstallResult{Err: "copy failed: " + err.Error()}
	}
	_ = src.Close()
	_ = tmp.Close()
	return installTempFile(tmp.Name(), version)
}

func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func installTempFile(tmpPath, version string) updateInstallResult {
	exePath, err := currentExecutablePath()
	if err != nil {
		_ = os.Remove(tmpPath)
		return updateInstallResult{Err: err.Error()}
	}

	// Magic-byte check: the source must be ELF/PE/Mach-O, not HTML or text.
	if !looksLikeBinary(tmpPath) {
		_ = os.Remove(tmpPath)
		return updateInstallResult{Err: "update source doesn't look like a binary for this platform"}
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return updateInstallResult{Err: "chmod failed: " + err.Error()}
	}

	// Swap. On Windows the running executable is locked by the OS, so a
	// direct rename fails with "Access is denied". Stage a cmd helper that
	// retries the replacement after the user closes Spore.
	if runtime.GOOS == "windows" {
		if err := scheduleWindowsReplacement(tmpPath, exePath); err != nil {
			_ = os.Remove(tmpPath)
			return updateInstallResult{Err: "cannot schedule Windows replacement: " + err.Error()}
		}
		return updateInstallResult{
			OK:      true,
			Version: version,
			Path:    exePath + " (scheduled; close Spore, then launch it again)",
		}
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Remove(tmpPath)
		return updateInstallResult{Err: "atomic rename failed: " + err.Error()}
	}
	return updateInstallResult{OK: true, Version: version, Path: exePath}
}

func scheduleWindowsReplacement(tmpPath, exePath string) error {
	dir := filepath.Dir(exePath)
	script, err := os.CreateTemp(dir, ".spore-update-*.cmd")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	oldPath := exePath + ".old"
	body := fmt.Sprintf(`@echo off
set "TMP=%s"
set "EXE=%s"
set "OLD=%s"
for /l %%%%i in (1,1,600) do (
  move /Y "%%EXE%%" "%%OLD%%" >nul 2>nul
  if not errorlevel 1 goto replace
  timeout /t 1 /nobreak >nul
)
exit /b 1
:replace
move /Y "%%TMP%%" "%%EXE%%" >nul 2>nul
if errorlevel 1 (
  move /Y "%%OLD%%" "%%EXE%%" >nul 2>nul
  exit /b 1
)
del "%%OLD%%" >nul 2>nul
del "%%~f0" >nul 2>nul
`, tmpPath, exePath, oldPath)
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd := exec.Command("cmd", "/C", "start", "", "/MIN", scriptPath)
	cmd.Dir = dir
	return cmd.Start()
}

func fetchLatestTag() (string, string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/yumlevi/spore-code/releases/latest", nil)
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
