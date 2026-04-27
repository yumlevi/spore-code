package tools

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWebServeStartStop boots a server, fetches a file from it via
// localhost, then stops. Sanity check that the local web_serve tool
// actually does what the agent expects when it calls it.
func TestWebServeStartStop(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("hi from acorn\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Start with auto-assigned port.
	r := WebServe(map[string]any{"action": "start", "dir": tmp}, tmp, "expanded")
	m, ok := r.(map[string]any)
	if !ok || m["ok"] != true {
		t.Fatalf("start failed: %+v", r)
	}
	port, _ := m["port"].(int)
	if port == 0 {
		t.Fatalf("expected non-zero port; got %v", m["port"])
	}
	id, _ := m["id"].(int)
	if id == 0 {
		t.Fatalf("expected non-zero id; got %v", m["id"])
	}

	// Give the server a moment to be ready.
	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		resp, err := http.Get(m["local_url"].(string) + "hello.txt")
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(body) != "hi from acorn\n" {
		t.Errorf("GET hello.txt: want body %q, got %q", "hi from acorn\n", string(body))
	}

	// Status reports the running server.
	stat := WebServe(map[string]any{"action": "status"}, tmp, "")
	sm, _ := stat.(map[string]any)
	servers, _ := sm["servers"].([]map[string]any)
	if len(servers) == 0 {
		t.Errorf("status reports 0 servers; expected 1")
	}

	// Stop by id.
	stopR := WebServe(map[string]any{"action": "stop", "id": id}, tmp, "")
	stopM, _ := stopR.(map[string]any)
	if stopM["ok"] != true {
		t.Errorf("stop failed: %+v", stopR)
	}

	// Subsequent GETs should fail (server gone). Allow ~500ms for
	// listener teardown.
	time.Sleep(200 * time.Millisecond)
	if _, err := http.Get(m["local_url"].(string) + "hello.txt"); err == nil {
		t.Errorf("expected GET to fail after stop; succeeded")
	}
}

// TestWebServeBackendActionFallsBack confirms action=backend returns
// a clear error pointing to exec, not a "not implemented" surprise.
func TestWebServeBackendActionFallsBack(t *testing.T) {
	r := WebServe(map[string]any{"action": "backend", "dir": "/tmp"}, "/tmp", "expanded")
	m, _ := r.(map[string]any)
	if m["ok"] != false {
		t.Fatalf("backend action should return ok=false; got %+v", r)
	}
	errStr, _ := m["error"].(string)
	if errStr == "" {
		t.Errorf("expected error string explaining backend isn't supported locally")
	}
}
