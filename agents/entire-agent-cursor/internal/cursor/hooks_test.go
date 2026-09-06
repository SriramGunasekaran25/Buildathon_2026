package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	promptPayload     = `{"hook_event_name":"beforeSubmitPrompt","conversation_id":"conv-123","generation_id":"gen-1","prompt":"Create hello.txt","model":"cursor-fast","workspace_roots":["/repo"]}`
	prompt2Payload    = `{"hook_event_name":"beforeSubmitPrompt","conversation_id":"conv-123","generation_id":"gen-2","prompt":"Now add a test","model":"cursor-fast"}`
	fileEditPayload   = `{"hook_event_name":"afterFileEdit","conversation_id":"conv-123","file_path":"/repo/hello.txt","edits":[{"old_string":"","new_string":"hello"}]}`
	stopPayload       = `{"hook_event_name":"stop","conversation_id":"conv-123","status":"completed","message":"Done, created hello.txt"}`
	sessionEndPayload = `{"hook_event_name":"sessionEnd","conversation_id":"conv-123"}`
)

func TestParseHookLifecycle(t *testing.T) {
	testRepo(t)
	a := New()

	// First prompt of the conversation → SessionStart.
	event, err := a.ParseHook(HookNameBeforeSubmitPrompt, []byte(promptPayload))
	if err != nil {
		t.Fatalf("ParseHook(before-submit-prompt): %v", err)
	}
	if event.Type != 1 {
		t.Errorf("first prompt type = %d, want 1 (SessionStart)", event.Type)
	}
	if event.SessionID != "conv-123" || event.Prompt != "Create hello.txt" {
		t.Errorf("event = %+v", event)
	}
	if event.Metadata["generation_id"] != "gen-1" {
		t.Errorf("metadata = %v", event.Metadata)
	}

	// Second prompt → TurnStart.
	event, err = a.ParseHook(HookNameBeforeSubmitPrompt, []byte(prompt2Payload))
	if err != nil {
		t.Fatalf("ParseHook(second prompt): %v", err)
	}
	if event.Type != 2 {
		t.Errorf("second prompt type = %d, want 2 (TurnStart)", event.Type)
	}

	// File edit → no protocol event, but recorded in the sidecar.
	event, err = a.ParseHook(HookNameAfterFileEdit, []byte(fileEditPayload))
	if err != nil {
		t.Fatalf("ParseHook(after-file-edit): %v", err)
	}
	if event != nil {
		t.Errorf("after-file-edit event = %+v, want nil", event)
	}

	// Stop → TurnEnd carrying the assistant response.
	event, err = a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook(stop): %v", err)
	}
	if event.Type != 3 {
		t.Errorf("stop type = %d, want 3 (TurnEnd)", event.Type)
	}
	if event.ResponseMessage != "Done, created hello.txt" {
		t.Errorf("response message = %q", event.ResponseMessage)
	}

	// Session end → SessionEnd and marker cleared.
	event, err = a.ParseHook(HookNameSessionEnd, []byte(sessionEndPayload))
	if err != nil {
		t.Fatalf("ParseHook(session-end): %v", err)
	}
	if event.Type != 5 {
		t.Errorf("session-end type = %d, want 5", event.Type)
	}

	// After a session-end the next prompt is treated as a fresh SessionStart.
	event, err = a.ParseHook(HookNameBeforeSubmitPrompt, []byte(promptPayload))
	if err != nil {
		t.Fatalf("ParseHook(post-end prompt): %v", err)
	}
	if event.Type != 1 {
		t.Errorf("post-end prompt type = %d, want 1", event.Type)
	}

	// Sidecar should contain the appended records.
	ref := a.sidecarPath("conv-123")
	data, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if pos := len(parseSidecar(data)); pos < 5 {
		t.Errorf("sidecar records = %d, want >= 5", pos)
	}
}

func TestParseHookIgnoresIrrelevantInput(t *testing.T) {
	testRepo(t)
	a := New()

	cases := map[string]string{
		"empty":                "",
		"whitespace":           "   \n",
		"missing conversation": `{"hook_event_name":"stop"}`,
		"unknown hook":         promptPayload, // paired with unknown-hook name below
	}
	for name, payload := range cases {
		hook := HookNameStop
		if name == "unknown hook" {
			hook = "totally-unknown"
		}
		event, err := a.ParseHook(hook, []byte(payload))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
		if event != nil && name != "unknown hook" {
			t.Errorf("%s: event = %+v, want nil", name, event)
		}
	}

	if _, err := a.ParseHook(HookNameStop, []byte("not json")); err == nil {
		t.Error("malformed JSON must error")
	}
	if _, err := a.ParseHook(HookNameStop, []byte(`{"conversation_id":"bad id!"}`)); err == nil {
		t.Error("unsafe session id must error")
	}
}

func TestInstallUninstallHooks(t *testing.T) {
	repo := testRepo(t)
	a := New()

	if a.AreHooksInstalled() {
		t.Fatal("hooks must not be installed initially")
	}

	count, err := a.InstallHooks(false, false)
	if err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if count != len(hookSpecs) {
		t.Errorf("installed = %d, want %d", count, len(hookSpecs))
	}
	if !a.AreHooksInstalled() {
		t.Fatal("AreHooksInstalled must be true after install")
	}

	hooksPath := filepath.Join(repo, ".cursor", hooksFileName)
	file := readHooksFile(hooksPath)
	if file.Version != hooksVersion {
		t.Errorf("version = %d", file.Version)
	}
	for _, spec := range hookSpecs {
		if !hasEntireHook(file.Hooks[spec.CursorEvent], hookCommandPrefix+spec.HookName) {
			t.Errorf("missing hook for %s", spec.CursorEvent)
		}
	}

	// Idempotent without force.
	count, err = a.InstallHooks(false, false)
	if err != nil || count != 0 {
		t.Errorf("idempotent install = (%d, %v), want (0, nil)", count, err)
	}

	if err := a.UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if a.AreHooksInstalled() {
		t.Error("hooks must be gone after uninstall")
	}
}

func TestInstallHooksPreservesForeignEntries(t *testing.T) {
	repo := testRepo(t)
	a := New()

	hooksPath := filepath.Join(repo, ".cursor", hooksFileName)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		t.Fatal(err)
	}
	foreign := `{"version":1,"hooks":{"beforeSubmitPrompt":[{"command":"my-linter","timeout":30}]},"telemetry":false}`
	if err := os.WriteFile(hooksPath, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.InstallHooks(false, true); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("hooks.json invalid: %v", err)
	}
	if _, ok := raw["telemetry"]; !ok {
		t.Error("foreign top-level key must be preserved")
	}
	file := readHooksFile(hooksPath)
	entries := file.Hooks["beforeSubmitPrompt"]
	if !hasEntireHook(entries, hookCommandPrefix+HookNameBeforeSubmitPrompt) {
		t.Error("entire hook must be added")
	}
	foundForeign := false
	for _, e := range entries {
		if e.command() == "my-linter" {
			foundForeign = true
			if _, ok := e["timeout"]; !ok {
				t.Error("foreign entry fields must be preserved")
			}
		}
	}
	if !foundForeign {
		t.Error("foreign hook entry must be preserved")
	}

	// Uninstall must remove only entire entries and keep the foreign linter.
	if err := a.UninstallHooks(); err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	file = readHooksFile(hooksPath)
	if a.AreHooksInstalled() {
		t.Error("entire hooks must be gone")
	}
	if !hasEntireHook(file.Hooks["beforeSubmitPrompt"], "my-linter") {
		t.Error("foreign linter hook must survive uninstall")
	}
}
