package tools

// web_serve has been intentionally disabled for acorn sessions. The
// server-side flavor hosts files from /workspace inside the SPORE
// container — useless when the agent's intent is "let me reach a file
// from my phone over LAN". For a minute we shipped a local
// implementation (v0.7.0/v0.7.1), but the operator decided agents
// should not call this tool at all in acorn sessions: every legitimate
// use case (host a static file, expose a backend) is better served by
// `exec` launching `python -m http.server`, `npx serve`, the
// project's own dev server, etc. — all of which run on the user's
// machine with their environment intact and don't rely on a separate
// tool with surprising semantics.
//
// The Go binary still claims web_serve via internal/tools/executor.go
// `localTools`. Claiming + refusing here is what blocks the call —
// if we let it fall through, SPORE's server-side _webServeTool would
// run inside the container with absurd results (Windows paths
// passed to a Linux fileserver, Traefik proxying to a host the user
// can't reach, etc.). The agent receives a clear error pointing at
// `exec` and re-routes.

// WebServe always refuses for acorn sessions and tells the agent to
// use exec instead. Kept as the single entry point so executor.go
// dispatch stays consistent with read_file / glob / grep style.
func WebServe(input map[string]any, cwd, scope string) any {
	_ = input
	_ = cwd
	_ = scope
	return map[string]any{
		"ok": false,
		"error": "web_serve is disabled for acorn sessions. " +
			"For local file serving use `exec`: e.g. `python3 -m http.server 8000` " +
			"in the directory you want to host, then share the LAN URL " +
			"(get the user's IP via `exec` calling `ipconfig` on Windows or " +
			"`ip -o addr show` on Linux). For backend processes use `exec` " +
			"with the framework's own `start` / `dev` command — it runs on " +
			"the user's machine with their full environment.",
		"hint": "use_exec_instead",
	}
}
