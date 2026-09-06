// Package cursor implements the Entire external agent protocol for the Cursor
// Agent CLI (cursor-agent). Cursor exposes a project-scoped hooks system in
// .cursor/hooks.json; this adapter installs Entire hook commands there and
// materializes a repo-scoped sidecar transcript so Entire can analyze prompts,
// modified files, and assistant responses. See AGENT.md for the full mapping.
package cursor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/external-agents/agents/entire-agent-cursor/internal/protocol"
)

// Agent implements the Entire external agent protocol for Cursor Agent.
type Agent struct {
	// LookPath resolves executables; overridable in tests.
	LookPath func(string) (string, error)
	// CheckpointExporter, when set, receives normalized checkpoints on
	// turn/session boundaries. When nil, the adapter builds one from the
	// ENTIRE_DATABRICKS_* environment on demand.
	CheckpointExporter CheckpointExporter
}

// New returns an Agent wired to real process lookups.
func New() *Agent {
	return &Agent{LookPath: exec.LookPath}
}

func (a *Agent) Info() protocol.InfoResponse {
	return protocol.InfoResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            AgentName,
		Type:            AgentType,
		Description:     "Cursor Agent CLI integration for Entire",
		IsPreview:       true,
		ProtectedDirs:   []string{".cursor"},
		ProtectedFiles:  []string{filepath.ToSlash(filepath.Join(".cursor", hooksFileName))},
		HookNames: []string{
			HookNameBeforeSubmitPrompt,
			HookNameAfterFileEdit,
			HookNameStop,
			HookNameSessionEnd,
		},
		Capabilities: protocol.DeclaredCapabilities{
			Hooks:              true,
			TranscriptAnalyzer: true,
			CompactTranscript:  true,
			UsesTerminal:       true,
		},
	}
}

func (a *Agent) Detect() protocol.DetectResponse {
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(cursorBinary)
	return protocol.DetectResponse{Present: err == nil}
}

func (a *Agent) GetSessionID(input *protocol.HookInputJSON) string {
	if input != nil && strings.TrimSpace(input.SessionID) != "" {
		return input.SessionID
	}
	return stubSessionID
}

// GetSessionDir returns a stable, repo-scoped directory in the OS temp root.
// Keeping it out of the repo tree avoids colliding with other agents' session
// dirs while remaining discoverable via the conversation id.
func (a *Agent) GetSessionDir(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		repoPath = protocol.RepoRoot()
	}
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		repoPath = resolved
	}
	sum := sha256.Sum256([]byte(repoPath))
	key := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(os.TempDir(), "entire-cursor", key), nil
}

func (a *Agent) ResolveSessionFile(sessionDir, sessionID string) string {
	if strings.TrimSpace(sessionDir) == "" {
		sessionDir, _ = a.GetSessionDir(protocol.RepoRoot())
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = stubSessionID
	}
	return filepath.Join(sessionDir, safeFilename(sessionID)+".jsonl")
}

func (a *Agent) FormatResumeCommand(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return "cursor-agent --continue"
	}
	return "cursor-agent --resume " + shellQuote(sessionID)
}

func (a *Agent) ReadSession(input *protocol.HookInputJSON) (protocol.AgentSessionJSON, error) {
	sessionID := a.GetSessionID(input)
	repoRoot := protocol.RepoRoot()

	var sessionRef string
	if input != nil && strings.TrimSpace(input.SessionRef) != "" {
		sessionRef = input.SessionRef
	} else {
		sessionDir, err := a.GetSessionDir(repoRoot)
		if err != nil {
			return protocol.AgentSessionJSON{}, err
		}
		sessionRef = a.ResolveSessionFile(sessionDir, sessionID)
	}

	var nativeData []byte
	if data, err := os.ReadFile(sessionRef); err == nil {
		nativeData = data
	}

	return protocol.AgentSessionJSON{
		SessionID:     sessionID,
		AgentName:     AgentName,
		RepoPath:      repoRoot,
		SessionRef:    sessionRef,
		StartTime:     time.Now().UTC().Format(time.RFC3339),
		NativeData:    nativeData,
		ModifiedFiles: modifiedFilesFromSidecar(nativeData, 0),
		NewFiles:      []string{},
		DeletedFiles:  []string{},
	}, nil
}

func (a *Agent) WriteSession(session protocol.AgentSessionJSON) error {
	if strings.TrimSpace(session.SessionRef) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(session.SessionRef), 0o700); err != nil {
		return err
	}
	return os.WriteFile(session.SessionRef, session.NativeData, 0o600)
}

func (a *Agent) ReadTranscript(sessionRef string) ([]byte, error) {
	return os.ReadFile(sessionRef)
}

func (a *Agent) ChunkTranscript(content []byte, maxSize int) ([][]byte, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("max-size must be positive, got %d", maxSize)
	}
	var chunks [][]byte
	for len(content) > 0 {
		end := min(maxSize, len(content))
		chunks = append(chunks, content[:end])
		content = content[end:]
	}
	return chunks, nil
}

func (a *Agent) ReassembleTranscript(chunks [][]byte) ([]byte, error) {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out, nil
}

func safeFilename(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return stubSessionID
	}
	return b.String()
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r == ':' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			safe = false
			break
		}
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
