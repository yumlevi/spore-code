// Package proto defines the WebSocket message shapes shared with the SPORE
// web gateway. Names mirror acorn/protocol.py and gateways/web.js.
//
// For inbound frames the app layer holds onto the raw JSON (via
// conn.Frame.Raw) and decodes specific subtypes on demand. We still keep
// typed helpers here so the hot path (chat streaming) doesn't churn alloc.
package proto

import (
	"encoding/json"
	"time"
)

// ChatStart — no fields we care about.
type ChatStart struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
}

// ChatDelta — streaming text chunk.
type ChatDelta struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	SessionID string `json:"sessionId,omitempty"`
}

// ChatThinking — <think> token chunk.
type ChatThinking struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ChatStatus — heartbeat / progress indicator during a turn.
// status ∈ {thinking_start, thinking, thinking_done,
//          tool_exec_start, tool_exec_done, interjection, interjected, waiting}
type ChatStatus struct {
	Type        string `json:"type"`
	Status      string `json:"status"`
	Tool        string `json:"tool,omitempty"`
	Detail      string `json:"detail,omitempty"`
	ResultChars int    `json:"resultChars,omitempty"`
	DurationMs  int    `json:"durationMs,omitempty"`
	Iteration   int    `json:"iteration,omitempty"`
	Tokens      int    `json:"tokens,omitempty"`
	Count       int    `json:"count,omitempty"`
}

// ChatTool — a tool call happened; UI may highlight.
type ChatTool struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	Tool string `json:"tool,omitempty"`
}

// ChatDone — end of turn with usage.
type ChatDone struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Iterations int            `json:"iterations,omitempty"`
	ToolUsage  map[string]int `json:"toolUsage,omitempty"`
}

type Usage struct {
	InputTokens            int `json:"input_tokens,omitempty"`
	OutputTokens           int `json:"output_tokens,omitempty"`
	CacheReadInputTokens   int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// ChatError — fatal error mid-turn.
type ChatError struct {
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// ChatHistory — replayed when joining a session.
type ChatHistory struct {
	Type      string           `json:"type"`
	SessionID string           `json:"sessionId,omitempty"`
	Messages  []HistoryMessage `json:"messages,omitempty"`
}

type HistoryMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	TS   time.Time `json:"-"`
}

// ChatBusy — server tells us the session is currently mid-turn (e.g. another tab started one).
type ChatBusy struct{ Type string `json:"type"` }

// ProjectContext is the structured "stuff the agent needs to know about
// the user's project" that we send as a sibling field on every chat:submit.
//
// It exists because the previous design glued this metadata onto the user
// message string, which caused SPORE to push it into messages[] and replay
// it in every subsequent API call. By sending it as a separate field, SPORE
// can route it into the system prompt (built fresh each turn, never
// accumulates) instead of the message history. See plan/spore-context.
//
// Sent on every message — SPORE is responsible for diffing against the
// previous turn's value and only re-rendering changed fields. Cheap on
// the wire (a small struct), big savings in the conversation token cost.
type ProjectContext struct {
	Cwd         string   `json:"cwd"`
	Project     string   `json:"project"`               // basename of git root or cwd
	GitBranch   string   `json:"gitBranch,omitempty"`   // current branch name
	GitStatus   string   `json:"gitStatus,omitempty"`   // git status --short, capped at 1KB
	GitHash     string   `json:"gitHash,omitempty"`     // HEAD short hash, used as cache key
	ProjectType string   `json:"projectType,omitempty"` // "Go", "Node.js", etc.
	AcornMd     string   `json:"acornMd,omitempty"`     // ACORN.md contents, capped at 4KB
	Tree        []string `json:"tree,omitempty"`        // depth-2 paths only, no contents
	Tools       []string `json:"tools,omitempty"`       // ["node", "go", "git", ...]
	Mode        string   `json:"mode,omitempty"`        // "plan" | "execute" — replaces PlanPrefix glue
	OS          string   `json:"os,omitempty"`          // runtime.GOOS
	Arch        string   `json:"arch,omitempty"`        // runtime.GOARCH

	// Scope governs file-op sandboxing. "strict" (default) locks
	// read_file/write_file/edit_file/exec to the cwd directory tree.
	// "expanded" tells the agent + the local executor that the user
	// has opted in to broader access — no cwd check, no sandbox
	// warning in the prompt. Toggled via /scope.
	Scope string `json:"scope,omitempty"` // "strict" | "expanded"

	// Hardware describes the user's machine — kernel + CPU + RAM + GPU.
	// Lets the agent reason about hardware-aware decisions: "you have
	// an RTX 4090, you can run a 70B local model"; "you're on Apple
	// Metal, prefer MLX over PyTorch"; "RAM is 8Gi, don't suggest a
	// docker-compose stack with 5 services". Detected once per session
	// and cached — see acorn/internal/app/context.go:detectHardware.
	// Optional — older acorns won't send it; SPORE renders it as a
	// "## Machine" sub-block in the Project Context section when
	// present.
	Hardware *Hardware `json:"hardware,omitempty"`
}

// Hardware is the optional machine-spec sub-struct on ProjectContext.
// All fields are best-effort — detectHardware silently skips probes
// that fail or aren't applicable to the current OS. Empty fields are
// elided from the JSON via omitempty so unsupported probes don't
// pollute the prompt.
type Hardware struct {
	Kernel   string   `json:"kernel,omitempty"`   // "Linux 5.10.28-Unraid", "Darwin 24.0.0", "Windows 10.0.19045"
	CPUModel string   `json:"cpuModel,omitempty"` // "AMD Ryzen 9 5950X 16-Core Processor"
	CPUCores int      `json:"cpuCores,omitempty"` // runtime.NumCPU()
	RAMGi    int      `json:"ramGi,omitempty"`    // total physical, GiB rounded
	GPU      []string `json:"gpu,omitempty"`      // ["NVIDIA RTX 4090 24576MiB driver 545.29.06", "CUDA 12.3"]
}

// ServerCapabilities — SPORE advertises feature support on connection.
// Sent as a `capabilities` frame from server to client right after the
// WS upgrade. acorn uses it to decide whether to send projectContext as
// a sibling field (new path) or fall back to gluing GatherContext onto
// the message content (old path).
type ServerCapabilities struct {
	Type           string `json:"type"`
	ProjectContext bool   `json:"projectContext,omitempty"` // routes projectContext into system prompt
	SporeVersion   string `json:"sporeVersion,omitempty"`
}

// ChatStopped / ChatCleared — /stop and /clear roundtrips.
type ChatStopped struct{ Type string `json:"type"` }
type ChatCleared struct{ Type string `json:"type"` }

// ToolRequest — server → CLI: "please execute this tool locally and send
// tool:result back." Matches acorn/tools/executor.py inputs.
type ToolRequest struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolAck — synchronous acknowledgment so server knows we're alive.
type ToolAck struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ToolResult — CLI → server with the executed result.
type ToolResult struct {
	Type   string      `json:"type"`
	ID     string      `json:"id"`
	Result any         `json:"result"`
}

// CodeView / CodeDiff — optional code viewer events streamed alongside
// read_file / edit_file tool runs.
type CodeView struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Language string `json:"language,omitempty"`
	IsNew    bool   `json:"isNew,omitempty"`
}

type CodeDiff struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// AskUser — new SPORE structured question tool.
type AskUser struct {
	Type     string   `json:"type"`
	QID      string   `json:"qid"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskUserAnswer — CLI → server.
type AskUserAnswer struct {
	Type   string `json:"type"`
	QID    string `json:"qid"`
	Answer string `json:"answer"`
}

// Plan-mode (web queue variant). Acorn uses its own prose-based plan flow
// so these just surface as system messages when seen.
type PlanProposal struct {
	Type       string `json:"type"`
	ProposalID int    `json:"proposalId"`
	Sequence   int    `json:"sequence"`
	Tool       string `json:"tool"`
	Summary    string `json:"summary"`
}

type PlanApplied struct {
	Type    string           `json:"type"`
	Results []ProposalResult `json:"results"`
}

type ProposalResult struct {
	ProposalID int    `json:"proposalId"`
	Tool       string `json:"tool"`
	OK         bool   `json:"ok"`
	Summary    string `json:"summary,omitempty"`
	Error      string `json:"error,omitempty"`
}

type PlanMode struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// ── Companion app observer protocol ───────────────────────────────────

// PlanShowApproval — CLI → peers: the agent produced a plan, observers
// should render an approval UI.
type PlanShowApproval struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PlanDecision — companion → CLI: execute / revise / cancel.
type PlanDecision struct {
	Type     string `json:"type"`
	Action   string `json:"action"`
	Feedback string `json:"feedback,omitempty"`
}

// StateQuestions — CLI → peers: we're collecting questions, mobile should
// show the same sheet.
type StateQuestions struct {
	Type      string         `json:"type"`
	Questions []QuestionItem `json:"questions"`
}

type QuestionItem struct {
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	Multi   bool     `json:"multi,omitempty"`
	Index   int      `json:"index,omitempty"`
}

// InteractiveResolved — CLI → peers: dismiss any active sheet for this kind.
type InteractiveResolved struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
}

// ToolAwaitingApproval — CLI → peers: a dangerous tool is waiting for the
// user to approve. Mobile can render a prompt and reply with ToolApprove.
type ToolAwaitingApproval struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Dangerous bool   `json:"dangerous"`
}

// ToolApprove — companion → CLI: allowed/denied.
type ToolApprove struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Allowed bool   `json:"allowed"`
}
