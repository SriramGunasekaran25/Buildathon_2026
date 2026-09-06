package cursor

const (
	// AgentName is the Entire registry name (binary is entire-agent-cursor).
	AgentName = "cursor"
	// AgentType is the human-readable product name.
	AgentType = "Cursor Agent"
	// cursorBinary is the Cursor Agent CLI executable name.
	cursorBinary = "cursor-agent"

	stubSessionID  = "cursor-session-000"
	hooksFileName  = "hooks.json"
	hooksVersion   = 1
	sidecarVersion = 1
)

// Entire hook verbs registered with the CLI. Each maps to a native Cursor
// hook event installed into .cursor/hooks.json.
const (
	HookNameBeforeSubmitPrompt = "before-submit-prompt"
	HookNameAfterFileEdit      = "after-file-edit"
	HookNameStop               = "stop"
	HookNameSessionEnd         = "session-end"
)

// hookSpec ties an Entire hook verb to the native Cursor event name that
// install-hooks writes into .cursor/hooks.json.
type hookSpec struct {
	CursorEvent string
	HookName    string
}

// hookSpecs is the stable, ordered mapping used for install/uninstall and for
// deterministic hooks.json output.
var hookSpecs = []hookSpec{
	{CursorEvent: "beforeSubmitPrompt", HookName: HookNameBeforeSubmitPrompt},
	{CursorEvent: "afterFileEdit", HookName: HookNameAfterFileEdit},
	{CursorEvent: "stop", HookName: HookNameStop},
	{CursorEvent: "sessionEnd", HookName: HookNameSessionEnd},
}

// cursorHookPayload is the JSON Cursor Agent writes to hook stdin. Field
// coverage is based on Cursor's documented hooks interface; unknown fields are
// ignored. Session identity is the conversation_id.
type cursorHookPayload struct {
	HookEventName  string        `json:"hook_event_name,omitempty"`
	ConversationID string        `json:"conversation_id,omitempty"`
	GenerationID   string        `json:"generation_id,omitempty"`
	WorkspaceRoots []string      `json:"workspace_roots,omitempty"`
	Prompt         string        `json:"prompt,omitempty"`
	Model          string        `json:"model,omitempty"`
	FilePath       string        `json:"file_path,omitempty"`
	Edits          []cursorEdit  `json:"edits,omitempty"`
	Status         string        `json:"status,omitempty"`
	Message        string        `json:"message,omitempty"`
	Attachments    []interface{} `json:"attachments,omitempty"`
}

type cursorEdit struct {
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

// sidecarRecord is the normalized, append-only JSONL Entire reads back. Cursor
// has no single durable transcript file this adapter can rely on, so the hook
// commands materialize one repo-scoped sidecar per session.
type sidecarRecord struct {
	V                int    `json:"v"`
	Agent            string `json:"agent"`
	Event            string `json:"event"`
	SessionID        string `json:"session_id"`
	TS               string `json:"ts"`
	Prompt           string `json:"prompt,omitempty"`
	Model            string `json:"model,omitempty"`
	FilePath         string `json:"file_path,omitempty"`
	AssistantMessage string `json:"assistant_message,omitempty"`
	Status           string `json:"status,omitempty"`
}
