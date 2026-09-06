package goose

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
	err        error
}

func (s *stubCheckpointExporter) ExportCheckpoint(_ context.Context, checkpoint DatabricksCheckpoint) (string, error) {
	s.checkpoint = checkpoint
	if s.err != nil {
		return "", s.err
	}
	return s.tableName, nil
}

func TestLoadDatabricksConfigFromEnvDisabledByDefault(t *testing.T) {
	cfg, enabled, err := loadDatabricksConfigFromEnv()
	if err != nil {
		t.Fatalf("loadDatabricksConfigFromEnv: %v", err)
	}
	if enabled {
		t.Fatalf("enabled = true, cfg = %+v", cfg)
	}
}

func TestLoadDatabricksConfigFromEnvRejectsPartialConfig(t *testing.T) {
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

func TestDatabricksExporterExecutesCreateAndInsert(t *testing.T) {
	var requests []statementExecutionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
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
			Host:        server.URL,
			Token:       "token",
			WarehouseID: "warehouse-1",
			Catalog:     "main",
			Schema:      "hackathon",
		},
		httpClient: server.Client(),
	}

	tableName, err := exporter.ExportCheckpoint(context.Background(), DatabricksCheckpoint{
		SessionID:           "20260611_1",
		RepoPath:            "/repo",
		RepositorySystem:    "entire",
		AgentName:           "goose",
		EventName:           "Stop",
		EventType:           3,
		Prompt:              "Implement this",
		Summary:             "Session summary",
		SessionRef:          "/tmp/20260611_1.json",
		Model:               "claude",
		Branch:              "main",
		HeadCommit:          "abc123",
		TranscriptAvailable: true,
		ModifiedFiles:       []string{"a.go", "b.go"},
		Metadata:            map[string]string{"source": "test"},
		CapturedAt:          "2026-09-06T05:30:00Z",
	})
	if err != nil {
		t.Fatalf("ExportCheckpoint: %v", err)
	}
	if tableName != "`main`.`hackathon`.`entire_agent_checkpoints`" {
		t.Fatalf("table name = %q", tableName)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[0].Statement, "CREATE TABLE IF NOT EXISTS `main`.`hackathon`.`entire_agent_checkpoints`") {
		t.Fatalf("create statement = %s", requests[0].Statement)
	}
	if !strings.Contains(requests[1].Statement, "INSERT INTO `main`.`hackathon`.`entire_agent_checkpoints`") {
		t.Fatalf("insert statement = %s", requests[1].Statement)
	}
	if !strings.Contains(requests[1].Statement, "'[\"\"a.go\"\",\"\"b.go\"\"]'") && !strings.Contains(requests[1].Statement, "'[\"a.go\",\"b.go\"]'") {
		t.Fatalf("insert statement missing modified files json: %s", requests[1].Statement)
	}
}

func TestParseHookAnnotatesDatabricksExportSuccess(t *testing.T) {
	exporter := &stubCheckpointExporter{tableName: "`main`.`hackathon`.`entire_agent_checkpoints`"}
	a := testAgent(t, &stubRunner{export: []byte(exportFixture)})
	a.CheckpointExporter = exporter

	event, err := a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if got := event.Metadata["databricks_export"]; got != "ok" {
		t.Fatalf("databricks_export = %q", got)
	}
	if got := event.Metadata["databricks_table"]; got != exporter.tableName {
		t.Fatalf("databricks_table = %q", got)
	}
	if exporter.checkpoint.RepositorySystem != "entire" {
		t.Fatalf("repository system = %q", exporter.checkpoint.RepositorySystem)
	}
	if len(exporter.checkpoint.ModifiedFiles) != 1 || exporter.checkpoint.ModifiedFiles[0] != "/tmp/workspace/verify.txt" {
		t.Fatalf("modified files = %v", exporter.checkpoint.ModifiedFiles)
	}
}

func TestParseHookAnnotatesDatabricksExportError(t *testing.T) {
	a := testAgent(t, &stubRunner{export: []byte(exportFixture)})
	a.CheckpointExporter = &stubCheckpointExporter{err: errors.New("warehouse unavailable")}

	event, err := a.ParseHook(HookNameStop, []byte(stopPayload))
	if err != nil {
		t.Fatalf("ParseHook: %v", err)
	}
	if got := event.Metadata["databricks_export"]; got != "export_error" {
		t.Fatalf("databricks_export = %q", got)
	}
	if !strings.Contains(event.Metadata["databricks_error"], "warehouse unavailable") {
		t.Fatalf("databricks_error = %q", event.Metadata["databricks_error"])
	}
}
