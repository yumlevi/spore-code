package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/yumlevi/spore-code/internal/app"
	"github.com/yumlevi/spore-code/internal/config"
)

// runSetupWizard is the Go port of acorn/config.py:run_setup_wizard.
// Writes ~/.spore-code/config.toml. Tests auth with the entered host/port/key
// before saving, offering the user a chance to continue anyway on failure.
func runSetupWizard() (*config.Config, error) {
	rd := bufio.NewReader(os.Stdin)
	home, _ := os.UserHomeDir()
	globalDir := filepath.Join(home, ".spore-code")

	// One-shot migration: legacy ~/.acorn/ → ~/.spore-code/. If the new
	// global dir doesn't exist yet but the old one does, copy contents
	// over and leave a MIGRATED.md breadcrumb so the operator can find
	// the old data if anything seems off after the rename. Best-effort:
	// failures here just mean the user goes through fresh setup.
	if home != "" {
		legacyDir := filepath.Join(home, ".acorn")
		if _, err := os.Stat(globalDir); os.IsNotExist(err) {
			if _, err := os.Stat(legacyDir); err == nil {
				if err := copyDirRecursive(legacyDir, globalDir); err == nil {
					_ = os.WriteFile(filepath.Join(legacyDir, "MIGRATED.md"),
						[]byte("# Migrated to ~/.spore-code/\n\nSpore Code (formerly acorn) renamed its global config dir to ~/.spore-code/.\nContents of this directory were copied there on the first run of `spore`. You can safely delete this directory once you've confirmed the new location works.\n"),
						0o644)
					fmt.Println()
					fmt.Println("Migrated ~/.acorn/ → ~/.spore-code/  (legacy dir kept; see ~/.acorn/MIGRATED.md)")
				}
			}
		}
	}

	fmt.Println()
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║  Spore Code — first-time setup          ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Println()

	// 1. Host + port
	fmt.Println("1. Connect to Anima")
	fmt.Println("   Enter your Anima server address.")
	fmt.Println("   Examples: 192.168.1.78 · https://acorn.example.com")
	host := prompt(rd, "   Host", "localhost")
	port := 18810
	if !strings.Contains(host, "://") {
		portStr := prompt(rd, "   Port", "18810")
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	fmt.Println()

	// 2. User
	fmt.Println("2. Your identity")
	fmt.Println("   Choose a username — the agent will remember you by this name.")
	user := ""
	for user == "" {
		user = strings.TrimSpace(prompt(rd, "   Username", ""))
		if user == "" {
			fmt.Println("   Username is required.")
		}
	}
	fmt.Println()

	// 3. Team key
	fmt.Println("3. Authentication")
	fmt.Println("   Enter the team key from your Anima server (.env ANIMA_ACORN_KEY value).")
	key := ""
	for key == "" {
		key = strings.TrimSpace(prompt(rd, "   Team key", ""))
		if key == "" {
			fmt.Println("   Team key is required.")
		}
	}
	fmt.Println()

	// 4. Test
	fmt.Println("4. Testing connection…")
	if err := testAuth(host, port, user, key); err != nil {
		fmt.Printf("   ✗ %s\n", err)
		if !confirm(rd, "   Continue anyway?", false) {
			return nil, fmt.Errorf("setup aborted")
		}
	} else {
		fmt.Println("   ✓ Connected and authenticated successfully.")
	}
	fmt.Println()

	// 5. Theme — show every theme with its icon and a swatch row so the
	// user can preview the palette before picking. Mirrors the Textual
	// wizard's theme step in acorn/setup.py.
	fmt.Println("5. Choose a theme")
	all := app.AllThemes()
	for _, t := range all {
		swatch := lipgloss.JoinHorizontal(lipgloss.Top,
			swatchCell(t.Accent),
			swatchCell(t.Accent2),
			swatchCell(t.Success),
			swatchCell(t.Warning),
			swatchCell(t.Error),
			swatchCell(t.Muted),
		)
		name := t.Name
		if t.Icon != "" {
			name = t.Icon + " " + name
		}
		// Pad the name column so swatches line up.
		fmt.Printf("   %-14s %s\n", name, swatch)
	}
	themes := app.ThemeNames()
	theme := prompt(rd, "   Theme", "dark")
	if !contains(themes, theme) {
		theme = "dark"
	}
	fmt.Println()

	// 6. Save
	cfg := &config.Config{
		Connection: config.ConnectionSection{Host: host, Port: port, User: user, Key: key},
		Display:    config.DisplaySection{Theme: theme},
		GlobalDir:  globalDir,
	}
	if err := config.Save(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	fmt.Printf("   ✓ Saved to %s\n\n", filepath.Join(globalDir, "config.toml"))
	return cfg, nil
}

func prompt(rd *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := rd.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return def
	}
	return line
}

func confirm(rd *bufio.Reader, label string, def bool) bool {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	fmt.Printf("%s %s: ", label, suffix)
	line, _ := rd.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// swatchCell renders one colored block for the wizard theme picker.
func swatchCell(c lipgloss.Color) string {
	return lipgloss.NewStyle().Background(c).Render("  ")
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// testAuth POSTs to /api/acorn/auth just to validate credentials. Mirrors
// connection.py:_sync_auth without establishing the WS.
func testAuth(host string, port int, user, key string) error {
	base := host
	if !strings.Contains(host, "://") {
		base = fmt.Sprintf("http://%s:%d", host, port)
	}
	base = strings.TrimRight(base, "/")
	payload, _ := json.Marshal(map[string]string{"username": user, "key": key})
	req, _ := http.NewRequestWithContext(
		context.Background(), "POST", base+"/api/acorn/auth", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}


// copyDirRecursive copies src → dst, creating dst if needed. Used for
// the one-shot ~/.acorn/ → ~/.spore-code/ migration on first run after
// the rebrand. Best-effort: errors propagate up so the wizard can fall
// through to fresh setup if the copy fails.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		// Skip the breadcrumb file if a previous run already wrote it.
		if filepath.Base(path) == "MIGRATED.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
