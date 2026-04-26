// Package config mirrors acorn/config.py — a global config at
// ~/.acorn/config.toml with [connection] and [display] sections, plus an
// optional per-project override at ./.acorn/config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type ConnectionSection struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	User string `toml:"user"`
	Key  string `toml:"key"`
}

type DisplaySection struct {
	Theme         string `toml:"theme"`
	ShowThinking  *bool  `toml:"show_thinking"`
	ShowTools     *bool  `toml:"show_tools"`
	ShowUsage     *bool  `toml:"show_usage"`
}

// SessionSection controls launch-time session behavior.
//
// AutoResume = true  → no-arg `acorn` resumes the deterministic
//                       (user, cwd)-keyed session, same as before. Useful
//                       for users who want to pick up where they left off
//                       on every launch.
// AutoResume = false → no-arg `acorn` opens a fresh, timestamped session.
//                       Use `acorn -c` to explicitly resume.
type SessionSection struct {
	AutoResume bool `toml:"auto_resume"`
}

type Config struct {
	Connection ConnectionSection `toml:"connection"`
	Display    DisplaySection    `toml:"display"`
	Session    SessionSection    `toml:"session"`

	// Resolved paths / runtime niceties (not serialized).
	GlobalDir string `toml:"-"`
	LocalDir  string `toml:"-"`
}

// Defaults.
const (
	DefaultHost = "localhost"
	DefaultPort = 18810
	DefaultTheme = "dark"
)

// Load reads global then per-project config; project overrides global.
// Returns an error if the global config doesn't exist (prompting the caller
// to run the setup wizard).
func Load(cwd string) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	globalDir := filepath.Join(home, ".acorn")
	globalPath := filepath.Join(globalDir, "config.toml")

	cfg := &Config{
		Connection: ConnectionSection{Host: DefaultHost, Port: DefaultPort},
		Display:    DisplaySection{Theme: DefaultTheme},
		GlobalDir:  globalDir,
		LocalDir:   filepath.Join(cwd, ".acorn"),
	}

	if _, err := os.Stat(globalPath); err != nil {
		if os.IsNotExist(err) {
			return cfg, ErrNoGlobalConfig
		}
		return cfg, err
	}
	if err := mergeFile(globalPath, cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", globalPath, err)
	}
	local := filepath.Join(cfg.LocalDir, "config.toml")
	if err := mergeFile(local, cfg); err != nil && !os.IsNotExist(err) {
		return cfg, fmt.Errorf("parse %s: %w", local, err)
	}
	return cfg, nil
}

// ErrNoGlobalConfig signals that the setup wizard needs to run.
var ErrNoGlobalConfig = errors.New("no global config — run setup wizard")

func mergeFile(path string, dst *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = toml.Decode(string(data), dst)
	return err
}

// Save writes the config to the global config.toml. Matches the layout the
// Python setup wizard produces.
func Save(cfg *Config) error {
	if err := os.MkdirAll(cfg.GlobalDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(cfg.GlobalDir, "config.toml")
	boolOrDefault := func(p *bool, def bool) bool {
		if p == nil {
			return def
		}
		return *p
	}
	out := fmt.Sprintf(`[connection]
host = %q
port = %d
user = %q
key = %q

[display]
theme = %q
show_thinking = %t
show_tools = %t
show_usage = %t

[session]
auto_resume = %t
`,
		cfg.Connection.Host, cfg.Connection.Port,
		cfg.Connection.User, cfg.Connection.Key,
		cfg.Display.Theme,
		boolOrDefault(cfg.Display.ShowThinking, true),
		boolOrDefault(cfg.Display.ShowTools, true),
		boolOrDefault(cfg.Display.ShowUsage, true),
		cfg.Session.AutoResume,
	)
	return os.WriteFile(path, []byte(out), 0o600)
}

// EnsureLocalDir creates ./.acorn/ with plans/ subdir, matching Python's
// ensure_local_dir.
func EnsureLocalDir(cwd string) error {
	p := filepath.Join(cwd, ".acorn", "plans")
	return os.MkdirAll(p, 0o755)
}

// LastSession records the most recent session id + its cwd so `acorn -c`
// can resume it.
type LastSession struct {
	SessionID string `toml:"session_id"`
	CWD       string `toml:"cwd"`
}

func LastSessionPath(globalDir string) string {
	return filepath.Join(globalDir, "last_session")
}

func SaveLastSession(globalDir, sessionID, cwd string) error {
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		return err
	}
	data := fmt.Sprintf("session_id = %q\ncwd = %q\n", sessionID, cwd)
	return os.WriteFile(LastSessionPath(globalDir), []byte(data), 0o600)
}

func LoadLastSession(globalDir string) (LastSession, error) {
	var s LastSession
	_, err := toml.DecodeFile(LastSessionPath(globalDir), &s)
	return s, err
}
