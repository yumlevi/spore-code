package tools

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// web_serve — local HTTP server that hosts a directory on the user's
// machine, reachable from their LAN. Replaces the server-side
// web_serve tool for acorn sessions, where the agent's intent is
// almost always "let me reach a file from my phone or another
// device" — which the SPORE container can't satisfy because it
// only has access to /workspace inside Docker.
//
// Actions:
//   start   — start a new server (or reuse existing if dir/port match)
//   stop    — stop a running server (by id, port, or all)
//   status  — list running servers + their LAN URLs
//   list    — alias for status
//
// Sandbox: dir must be inside cwd unless scope == "expanded".
// Path resolution mirrors fileops.ResolvePathScoped so the same
// /scope toggle the user already knows controls this too.

// webServer tracks a single in-process HTTP server.
type webServer struct {
	id     int
	addr   string // host:port we bind on
	port   int
	dir    string // absolute path being served
	server *http.Server
	startedAt time.Time
}

var (
	webServersMu sync.Mutex
	webServers   = map[int]*webServer{}
	nextWebID    = 1
)

// WebServe is the agent-callable entry point. Always returns a
// JSON-friendly map; never panics.
func WebServe(input map[string]any, cwd, scope string) any {
	action := strings.ToLower(strings.TrimSpace(asString(input["action"], "status")))

	switch action {
	case "start":
		return webServeStart(input, cwd, scope)
	case "stop":
		return webServeStop(input)
	case "status", "list":
		return webServeStatus()
	case "backend":
		// The server-side web_serve has a "backend" mode that
		// launches a process with vault keys auto-injected. The
		// local version doesn't replicate that — for backend
		// processes the agent should use exec directly. Return a
		// clear error so the agent re-routes.
		return errMap("web_serve action='backend' is a server-side feature; for acorn sessions, launch your backend via `exec` (it runs locally on your machine and inherits your environment).")
	}
	return errMap("unknown action: " + action + " (use start | stop | status)")
}

func webServeStart(input map[string]any, cwd, scope string) any {
	dirRaw := asString(input["dir"], cwd)
	if dirRaw == "" {
		dirRaw = cwd
	}
	dir, err := ResolvePathScoped(dirRaw, cwd, scope)
	if err != nil {
		return errMap(err.Error())
	}
	st, err := os.Stat(dir)
	if err != nil {
		return errMap("dir not found: " + dir)
	}
	if !st.IsDir() {
		return errMap("dir is not a directory: " + dir)
	}

	// Reuse an existing server if it's already serving the same dir.
	webServersMu.Lock()
	for _, ws := range webServers {
		if ws.dir == dir {
			webServersMu.Unlock()
			return webServerStatusJSON(ws, "already running")
		}
	}
	webServersMu.Unlock()

	port := asInt(input["port"], 0)
	listener, addr, gotPort, lErr := bindLocal(port)
	if lErr != nil {
		return errMap("listen: " + lErr.Error())
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(dir)))
	server := &http.Server{Handler: mux}

	webServersMu.Lock()
	id := nextWebID
	nextWebID++
	ws := &webServer{
		id:        id,
		addr:      addr,
		port:      gotPort,
		dir:       dir,
		server:    server,
		startedAt: time.Now(),
	}
	webServers[id] = ws
	webServersMu.Unlock()

	go func() {
		_ = server.Serve(listener) // blocks until Shutdown/Close
		webServersMu.Lock()
		delete(webServers, id)
		webServersMu.Unlock()
	}()

	return webServerStatusJSON(ws, "started")
}

func webServeStop(input map[string]any) any {
	idArg := asInt(input["id"], 0)
	portArg := asInt(input["port"], 0)
	all := asBool(input["all"], false)

	webServersMu.Lock()
	defer webServersMu.Unlock()

	if all {
		stopped := []int{}
		for id, ws := range webServers {
			_ = ws.server.Close()
			stopped = append(stopped, id)
		}
		webServers = map[int]*webServer{}
		sort.Ints(stopped)
		return map[string]any{"ok": true, "stopped": stopped}
	}

	if idArg > 0 {
		ws, ok := webServers[idArg]
		if !ok {
			return errMap(fmt.Sprintf("no server with id=%d", idArg))
		}
		_ = ws.server.Close()
		delete(webServers, idArg)
		return map[string]any{"ok": true, "stopped_id": idArg}
	}

	if portArg > 0 {
		var hit *webServer
		for _, ws := range webServers {
			if ws.port == portArg {
				hit = ws
				break
			}
		}
		if hit == nil {
			return errMap(fmt.Sprintf("no server bound on port %d", portArg))
		}
		_ = hit.server.Close()
		delete(webServers, hit.id)
		return map[string]any{"ok": true, "stopped_id": hit.id, "stopped_port": hit.port}
	}

	// Default: stop the most recent.
	if len(webServers) == 0 {
		return map[string]any{"ok": true, "note": "no servers running"}
	}
	var newest *webServer
	for _, ws := range webServers {
		if newest == nil || ws.startedAt.After(newest.startedAt) {
			newest = ws
		}
	}
	_ = newest.server.Close()
	delete(webServers, newest.id)
	return map[string]any{"ok": true, "stopped_id": newest.id, "stopped_port": newest.port}
}

func webServeStatus() any {
	webServersMu.Lock()
	defer webServersMu.Unlock()
	if len(webServers) == 0 {
		return map[string]any{"ok": true, "servers": []any{}, "note": "no servers running"}
	}
	out := make([]map[string]any, 0, len(webServers))
	ids := make([]int, 0, len(webServers))
	for id := range webServers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		ws := webServers[id]
		out = append(out, webServerStatusFields(ws))
	}
	return map[string]any{"ok": true, "servers": out}
}

func webServerStatusJSON(ws *webServer, note string) any {
	out := webServerStatusFields(ws)
	out["ok"] = true
	out["note"] = note
	return out
}

func webServerStatusFields(ws *webServer) map[string]any {
	urls := lanURLs(ws.port)
	return map[string]any{
		"id":         ws.id,
		"port":       ws.port,
		"dir":        ws.dir,
		"local_url":  fmt.Sprintf("http://localhost:%d/", ws.port),
		"lan_urls":   urls,
		"started_at": ws.startedAt.Format(time.RFC3339),
		"hint":       "Hit any LAN URL from another device on the same network to fetch files from this directory.",
	}
}

// bindLocal opens a TCP listener on the given port (or auto-assigns
// when port=0). Bound to 0.0.0.0 so peers on the same LAN can reach
// the server.
func bindLocal(port int) (net.Listener, string, int, error) {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", 0, err
	}
	tcpAddr, _ := ln.Addr().(*net.TCPAddr)
	gotPort := port
	if tcpAddr != nil {
		gotPort = tcpAddr.Port
	}
	return ln, addr, gotPort, nil
}

// lanURLs returns a list of "http://<lan-ip>:<port>/" the user can
// scan/share. Filters to non-loopback IPv4 addresses on up
// interfaces. Best-effort — returns at least the wildcard URL when
// detection fails so the agent always has something to show.
func lanURLs(port int) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{fmt.Sprintf("http://0.0.0.0:%d/", port)}
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ip.IP.To4()
			if v4 == nil {
				continue
			}
			ips = append(ips, fmt.Sprintf("http://%s:%d/", v4.String(), port))
		}
	}
	if len(ips) == 0 {
		ips = []string{fmt.Sprintf("http://0.0.0.0:%d/", port)}
	}
	sort.Strings(ips)
	return ips
}

// ensure ResolvePathScoped accepts these arg shapes; this is a guard
// only, not a runtime constraint.
var _ = filepath.Join
