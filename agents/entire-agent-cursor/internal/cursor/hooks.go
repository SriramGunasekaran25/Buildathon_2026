package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/external-agents/agents/entire-agent-cursor/internal/protocol"
)

const hookCommandPrefix = "entire hooks cursor "

// ParseHook converts a raw Cursor hook payload into a normalized Entire event
// and appends a record to the repo-scoped sidecar transcript. Cursor fires no
// dedicated session-start hook, so the first prompt of a conversation is
// mapped to SessionStart and later prompts to TurnStart, tracked via a
// per-session marker file.
func (a *Agent) ParseHook(hookName string, input []byte) (*protocol.EventJSON, error) {
	if len(bytes.TrimSpace(input)) == 0 {
		return nil, nil
	}

	var payload cursorHookPayload
	if err := json.Unmarshal(input, &payload); err != nil {
		return nil, fmt.Errorf("parse hook payload: %w", err)
	}

	sessionID := strings.TrimSpace(payload.ConversationID)
	if sessionID == "" {
		return nil, nil
	}
	if !validSessionID(sessionID) {
		return nil, fmt.Errorf("unsafe session id %q", sessionID)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sessionRef := a.sidecarPath(sessionID)

	record := sidecarRecord{
		V:         sidecarVersion,
		Agent:     AgentName,
		Event:     hookName,
		SessionID: sessionID,
		TS:        now,
		Prompt:    payload.Prompt,
		Model:     payload.Model,
		FilePath:  payload.FilePath,
		Status:    payload.Status,
	}
	if payload.Message != "" {
		record.AssistantMessage = payload.Message
	}
	if err := a.appendSidecar(sessionID, record); err != nil {
		return nil, err
	}

	metadata := map[string]string{}
	if payload.GenerationID != "" {
		metadata["generation_id"] = payload.GenerationID
	}

	switch hookName {
	case HookNameBeforeSubmitPrompt:
		firstTurn, err := a.markSessionStarted(sessionID)
		if err != nil {
			return nil, err
		}
		event := &protocol.EventJSON{
			SessionID:  sessionID,
			SessionRef: sessionRef,
			Prompt:     payload.Prompt,
			Model:      payload.Model,
			Timestamp:  now,
			Metadata:   metadata,
		}
		if firstTurn {
			event.Type = 1 // SessionStart
		} else {
			event.Type = 2 // TurnStart
		}
		a.annotateDatabricksExport(event, payload)
		return event, nil

	case HookNameStop:
		event := &protocol.EventJSON{
			Type:            3, // TurnEnd
			SessionID:       sessionID,
			SessionRef:      sessionRef,
			Model:           payload.Model,
			Timestamp:       now,
			ResponseMessage: payload.Message,
			Metadata:        metadata,
		}
		a.annotateDatabricksExport(event, payload)
		return event, nil

	case HookNameSessionEnd:
		_ = a.clearSessionMarker(sessionID)
		event := &protocol.EventJSON{
			Type:       5, // SessionEnd
			SessionID:  sessionID,
			SessionRef: sessionRef,
			Timestamp:  now,
			Metadata:   metadata,
		}
		a.annotateDatabricksExport(event, payload)
		return event, nil

	case HookNameAfterFileEdit:
		// File edits enrich the transcript for modified-file analysis but do
		// not open or close an Entire turn.
		return nil, nil

	default:
		return nil, nil
	}
}

func (a *Agent) sidecarDir() string {
	dir, _ := a.GetSessionDir(protocol.RepoRoot())
	return dir
}

func (a *Agent) sidecarPath(sessionID string) string {
	return filepath.Join(a.sidecarDir(), safeFilename(sessionID)+".jsonl")
}

func (a *Agent) markerPath(sessionID string) string {
	return filepath.Join(a.sidecarDir(), safeFilename(sessionID)+".started")
}

func (a *Agent) appendSidecar(sessionID string, record sidecarRecord) error {
	if err := ensurePrivateDir(a.sidecarDir()); err != nil {
		return err
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal sidecar record: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(a.sidecarPath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open sidecar: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append sidecar: %w", err)
	}
	return nil
}

// markSessionStarted returns true when this is the first prompt seen for the
// session (i.e. it should map to SessionStart), creating the marker as a side
// effect. Subsequent calls return false.
func (a *Agent) markSessionStarted(sessionID string) (bool, error) {
	if err := ensurePrivateDir(a.sidecarDir()); err != nil {
		return false, err
	}
	path := a.markerPath(sessionID)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func (a *Agent) clearSessionMarker(sessionID string) error {
	err := os.Remove(a.markerPath(sessionID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// InstallHooks writes .cursor/hooks.json wiring Cursor's native events to the
// Entire CLI while preserving any foreign hook entries the user configured.
func (a *Agent) InstallHooks(_ bool, force bool) (int, error) {
	if !force && a.AreHooksInstalled() {
		return 0, nil
	}
	repoRoot := protocol.RepoRoot()
	hooksPath := filepath.Join(repoRoot, ".cursor", hooksFileName)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return 0, err
	}

	file := readHooksFile(hooksPath)
	if file.Version == 0 {
		file.Version = hooksVersion
	}
	if file.Hooks == nil {
		file.Hooks = map[string][]hookEntry{}
	}
	for _, spec := range hookSpecs {
		file.Hooks[spec.CursorEvent] = upsertEntireHook(file.Hooks[spec.CursorEvent], hookCommandPrefix+spec.HookName)
	}

	content, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return 0, err
	}
	content = append(content, '\n')
	if err := os.WriteFile(hooksPath, content, 0o600); err != nil {
		return 0, err
	}
	return len(hookSpecs), nil
}

func (a *Agent) UninstallHooks() error {
	repoRoot := protocol.RepoRoot()
	hooksPath := filepath.Join(repoRoot, ".cursor", hooksFileName)
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file hooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse .cursor/hooks.json: %w", err)
	}

	remaining := 0
	for _, spec := range hookSpecs {
		entries := removeEntireHook(file.Hooks[spec.CursorEvent], hookCommandPrefix+spec.HookName)
		if len(entries) == 0 {
			delete(file.Hooks, spec.CursorEvent)
			continue
		}
		file.Hooks[spec.CursorEvent] = entries
	}
	for _, entries := range file.Hooks {
		remaining += len(entries)
	}

	// If nothing else remains, remove the file (and the now-empty .cursor dir
	// when we created it), otherwise write back the preserved entries.
	if remaining == 0 && len(file.Hooks) == 0 {
		if err := os.Remove(hooksPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(filepath.Join(repoRoot, ".cursor"))
		return nil
	}
	content, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(hooksPath, content, 0o600)
}

func (a *Agent) AreHooksInstalled() bool {
	hooksPath := filepath.Join(protocol.RepoRoot(), ".cursor", hooksFileName)
	file := readHooksFile(hooksPath)
	if file.Hooks == nil {
		return false
	}
	for _, spec := range hookSpecs {
		if !hasEntireHook(file.Hooks[spec.CursorEvent], hookCommandPrefix+spec.HookName) {
			return false
		}
	}
	return true
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create cursor private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect cursor private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("cursor private path is not a directory")
	}
	return nil
}

func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
