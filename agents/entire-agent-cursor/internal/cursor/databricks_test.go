package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubCheckpointExporter struct {
	tableName  string
	checkpoint DatabricksCheckpoint
	calls      int
	err        error
}

func (s *stubCheckpointExporter) ExportCheckpoint(_ context.Context, checkpoint DatabricksCheckpoint) (string, error) {
	s.calls++
	s.checkpoint = checkpoint
	if s.err != nil {
		return "", s.err
	}
	return s.tableName, nil
}

func TestLoadDatabricksConfigDisabledByDefault(t *testing.T) {
	testRepo(t)
	_, enabled, err := loadDatabricksConfigFromEnv()
	if err != nil {
		t.Fatalf("loadDatabricksConfigFromEnv: %v", err)
	}
	if enabled {
		t.Fatal("Databricks must be disabled when no env is set")
	}
}

func TestLoadDatabricksConfigRejectsPartial(t *testing.T) {
	testRepo(t)
	t.Setenv(databricksHostEnv, "https://dbc.example.com")
	t.Setenv(databricksTokenEnv, "token")

	_, enabled, err := loadDatabricksConfigFromEnv()
	if err == nil {
		t.Fatal("expected partial config error")
	}
	if enabled {
		t.Fatal("partial config must not enable exporter")
	}
}

func TestDatabricksExporterCreatesAndInserts(t *testing.T) {
	var requests []statementExecutionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("authorization = %q", got)
		}
		var req statementExecutionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, req)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":{"state":"SUCCEEDED"}}`))
	}))
	defer server.Close()

	exporter := &databricksExporter{
		cfg: databricksConfig{
			Host: server.URL, Token: "token", WarehouseID: "wh-1",
			Catalog: "main", Schema: "hackathon",
		},
		httpClient: server.Client(),
	}

	table, err := exporter.ExportCheckpoint(context.Background(), DatabricksCheckpoint{
		SessionID: "conv-123", RepoPath: "/repo", RepositorySystem: "entire",
		AgentName: "cursor", EventName: "stop", EventType: 3,
		Prompt: "Create hello.txt", Summary: "Created hello.txt",
		SessionRef: "/tmp/conv-123.jsonl", Model: "cursor-fast",
		Branch: "main", HeadCommit: "abc123", TranscriptAvailable: true,
		ModifiedFiles: []string{"/repo/hello.txt"},
		Metadata:      map[string]string{"generation_id": "gen-1"},
		CapturedAt:    "2026-09-06T05:00:05Z",
	})
	if err != nil {
		t.Fatalf("ExportCheckpoint: %v", err)
	}
	if table != "`main`.`hackathon`.`entire_agent_checkpoints`" {
		t.Fatalf("table = %q", table)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 (create + insert)", len(requests))
	}
	if !strings.HasPrefix(requests[0].Statement, "CREATE TABLE IF NOT EXISTS") {
		t.Errorf("first statement = %s", requests[0].Statement)
	}
	if !strings.Contains(requests[1].Statement, "INSERT INTO `main`.`hackathon`.`entire_agent_checkpoints`") {
		t.Errorf("insert statement = %s", requests[1].Statement)
	}
	if !strings.Contains(requests[1].Statement, "'conv-123'") {
		t.Errorf("insert missing session id: %s", requests[1].Statement)
	}
}

func TestDatabricksExporterEscapesQuotes(t *testing.T) {
	var insert string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req statementExecutionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.HasPrefix(req.Statement, "INSERT") {
			insert = req.Statement
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":{"state":"SUCCEEDED"}}`))
	}))
	defer server.Close()

	exporter := &databricksExporter{
		cfg:        databricksConfig{Host: server.URL, Token: "t", WarehouseID: "wh", Catalog: "c", Schema: "s"},
		httpClient: server.Client(),
	}
	if _, err := exporter.ExportCheckpoint(context.Background(), DatabricksCheckpoint{
		SessionID: "conv-1", RepositorySystem: "entire", AgentName: "cursor",
		EventName: "stop", EventType: 3, Prompt: "don't break", ModifiedFiles: []string{},
		Metadata: map[string]string{}, CapturedAt: "2026-09-06T05:00:05Z",
	}); err != nil {
		t.Fatalf("ExportCheckpoint: %v", err)
	}
	if !strings.Contains(insert, "'don''t break'") {
		t.Errorf("single quotes must be escaped: %s", insert)
	}
}

func TestDatabricksExporterSurfacesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad warehouse"}`))
	}))
	defer server.Close()

	exporter := &databricksExporter{
		cfg:        databricksConfig{Host: server.URL, Token: "t", WarehouseID: "wh", Catalog: "c", Schema: "s"},
		httpClient: server.Client(),
	}
	_, err := exporter.ExportCheckpoint(context.Background(), DatabricksCheckpoint{
		ModifiedFiles: []string{}, Metadata: map[string]string{},
	})
	if err == nil || !strings.Contains(err.Error(), "bad warehouse") {
		t.Fatalf("expected HTTP error surfaced, got %v", err)
	}
}

func TestParseHookAnnotatesDatabricksSuccess(t *testing.T) {
	testRepo(t)
	exporter := &stubCheckpointExporter{tableName: "`main`.`hackathon`.`entire_agent_checkpoints`"}
	a := New()
	a.CheckpointExporter = exporter

	// Seed the sidecar with a prompt + edit so the checkpoint carries context.
	if _, err := a.ParseHook(HookNameBeforeSubmitPrompt, []byte(promptPayload)); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	if _, err := a.ParseHook(HookNameAfterFileEdit, []byte(fileEditPayload)); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	event, err := a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook(stop): %v", err)
	}
	if got := event.Metadata["databricks_export"]; got != "ok" {
		t.Fatalf("databricks_export = %q", got)
	}
	if event.Metadata["databricks_table"] != exporter.tableName {
		t.Errorf("databricks_table = %q", event.Metadata["databricks_table"])
	}
	cp := exporter.checkpoint
	if cp.RepositorySystem != "entire" || cp.AgentName != "cursor" {
		t.Errorf("checkpoint identity = %+v", cp)
	}
	if !cp.TranscriptAvailable {
		t.Error("transcript should be available")
	}
	if cp.Summary != "Done, created hello.txt" {
		t.Errorf("summary = %q", cp.Summary)
	}
	if len(cp.ModifiedFiles) != 1 || cp.ModifiedFiles[0] != "/repo/hello.txt" {
		t.Errorf("modified files = %v", cp.ModifiedFiles)
	}
}

func TestParseHookAnnotatesDatabricksError(t *testing.T) {
	testRepo(t)
	a := New()
	a.CheckpointExporter = &stubCheckpointExporter{err: errors.New("warehouse unavailable")}

	if _, err := a.ParseHook(HookNameBeforeSubmitPrompt, []byte(promptPayload)); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	event, err := a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook(stop): %v", err)
	}
	if event.Metadata["databricks_export"] != "export_error" {
		t.Fatalf("databricks_export = %q", event.Metadata["databricks_export"])
	}
	if !strings.Contains(event.Metadata["databricks_error"], "warehouse unavailable") {
		t.Errorf("databricks_error = %q", event.Metadata["databricks_error"])
	}
}

func TestParseHookWithoutDatabricksLeavesNoExportMetadata(t *testing.T) {
	testRepo(t)
	a := New()
	if _, err := a.ParseHook(HookNameBeforeSubmitPrompt, []byte(promptPayload)); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}
	event, err := a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook(stop): %v", err)
	}
	if _, ok := event.Metadata["databricks_export"]; ok {
		t.Errorf("no databricks metadata expected when disabled: %v", event.Metadata)
	}
}
