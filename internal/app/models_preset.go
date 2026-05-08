package app

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/spore-code/internal/config"
)

// presetsFetchedMsg carries the list of preset names from the backend.
type presetsFetchedMsg struct {
	names  []string
	err    error
	silent bool
}

func fetchPresets(m *Model) tea.Msg {
	return fetchPresetsWithMode(m, false)
}

func fetchPresetsSilently(m *Model) tea.Msg {
	return fetchPresetsWithMode(m, true)
}

func fetchPresetsWithMode(m *Model, silent bool) tea.Msg {
	base := m.baseURL()
	if !authTransportAllowed(base) {
		return presetsFetchedMsg{err: fmt.Errorf("refusing to fetch presets over insecure HTTP to %s", base), silent: silent}
	}

	token := m.authToken()
	if token == "" {
		return presetsFetchedMsg{err: fmt.Errorf("no auth token available for preset fetch"), silent: silent}
	}

	req, err := http.NewRequest("GET", base+"/api/models/routing-presets", nil)
	if err != nil {
		return presetsFetchedMsg{err: err, silent: silent}
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return presetsFetchedMsg{err: err, silent: silent}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return presetsFetchedMsg{err: fmt.Errorf("fetch presets: HTTP %d", resp.StatusCode), silent: silent}
	}

	var result struct {
		OK      bool `json:"ok"`
		Presets []struct {
			Name string `json:"name"`
		} `json:"presets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return presetsFetchedMsg{err: err, silent: silent}
	}

	names := make([]string, 0, len(result.Presets))
	for _, p := range result.Presets {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	return presetsFetchedMsg{names: names, silent: silent}
}

// presetsAppliedMsg is sent after applying a preset via the backend.
type presetsAppliedMsg struct {
	name string
	err  error
}

func applyPresetCmd(m *Model, name string) tea.Cmd {
	return func() tea.Msg {
		return presetsAppliedMsg{
			name: name,
			err:  doApplyPreset(m, name),
		}
	}
}

func doApplyPreset(m *Model, name string) error {
	base := m.baseURL()
	if !authTransportAllowed(base) {
		return fmt.Errorf("refusing to apply preset over insecure HTTP to %s", base)
	}

	token := m.authToken()
	if token == "" {
		return fmt.Errorf("no auth token available")
	}

	req, err := http.NewRequest("POST", base+"/api/models/routing-presets/"+name+"/apply", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apply preset %q: HTTP %d", name, resp.StatusCode)
	}
	return nil
}

// handleModelsPreset processes /models_preset <NAME>.
func handleModelsPreset(m *Model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		if len(m.presetNames) == 0 {
			m.pushChat("system", "No presets loaded. Fetching...")
			return m, func() tea.Msg { return fetchPresets(m) }
		}
		list := strings.Join(m.presetNames, ", ")
		m.pushChat("system", fmt.Sprintf("Available presets: %s\nUsage: /models_preset <name>", list))
		return m, nil
	}

	name := args[0]
	m.pushChat("system", "Applying preset: "+name)
	return m, applyPresetCmd(m, name)
}

// authTransportAllowed mirrors conn/authTransportAllowed to avoid importing conn
// from app (which already imports conn indirectly).
func authTransportAllowed(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	env := strings.TrimSpace(os.Getenv("SPORE_CODE_ALLOW_INSECURE_AUTH"))
	return strings.EqualFold(env, "true") || env == "1"
}

// ── helpers on Model ──

func (m *Model) baseURL() string {
	base := m.cfg.Connection.Host
	if !strings.Contains(base, "://") {
		base = fmt.Sprintf("http://%s:%d", base, m.cfg.Connection.Port)
	}
	return strings.TrimRight(base, "/")
}

func (m *Model) authToken() string {
	if m.cfg.Connection.Method() == config.AuthDevice {
		if tok, err := config.LoadDeviceToken(m.cfg); err == nil {
			return tok
		}
	}
	if m.cfg.Connection.Method() == config.AuthInvite {
		return m.cfg.Connection.Key
	}
	return ""
}

// ── slash command registration ──

func init() {
	register(&slashCmd{
		Name:    "/models_preset",
		Help:    "List or apply a model routing preset (e.g. /models_preset fast)",
		Handler: handleModelsPreset,
	})
}
