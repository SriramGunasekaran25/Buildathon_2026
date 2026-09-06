package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/entireio/external-agents/agents/entire-agent-cursor/internal/protocol"
)

const (
	databricksHostEnv        = "ENTIRE_DATABRICKS_HOST"
	databricksTokenEnv       = "ENTIRE_DATABRICKS_TOKEN"
	databricksWarehouseEnv   = "ENTIRE_DATABRICKS_WAREHOUSE_ID"
	databricksCatalogEnv     = "ENTIRE_DATABRICKS_CATALOG"
	databricksSchemaEnv      = "ENTIRE_DATABRICKS_SCHEMA"
	databricksTableName      = "entire_agent_checkpoints"
	databricksStatementRoute = "/api/2.0/sql/statements"
)

// CheckpointExporter persists a normalized checkpoint to a durable store and
// returns a human-readable location for it.
type CheckpointExporter interface {
	ExportCheckpoint(context.Context, DatabricksCheckpoint) (string, error)
}

// DatabricksCheckpoint is the normalized Entire session context mirrored into
// Databricks on turn/session boundaries.
type DatabricksCheckpoint struct {
	SessionID           string
	RepoPath            string
	RepositorySystem    string
	AgentName           string
	EventName           string
	EventType           int
	Prompt              string
	Summary             string
	SessionRef          string
	Model               string
	Branch              string
	HeadCommit          string
	TranscriptAvailable bool
	ModifiedFiles       []string
	Metadata            map[string]string
	CapturedAt          string
}

type databricksConfig struct {
	Host        string
	Token       string
	WarehouseID string
	Catalog     string
	Schema      string
}

type databricksExporter struct {
	cfg        databricksConfig
	httpClient *http.Client
}

type statementExecutionRequest struct {
	WarehouseID string `json:"warehouse_id"`
	Statement   string `json:"statement"`
	WaitTimeout string `json:"wait_timeout,omitempty"`
}

type statementExecutionResponse struct {
	Status struct {
		State        string `json:"state"`
		ErrorMessage string `json:"error_message"`
	} `json:"status"`
}

func (a *Agent) checkpointExporter() (CheckpointExporter, error) {
	if a.CheckpointExporter != nil {
		return a.CheckpointExporter, nil
	}
	return newDatabricksExporterFromEnv(nil)
}

func newDatabricksExporterFromEnv(httpClient *http.Client) (CheckpointExporter, error) {
	cfg, enabled, err := loadDatabricksConfigFromEnv()
	if err != nil || !enabled {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &databricksExporter{cfg: cfg, httpClient: httpClient}, nil
}

func loadDatabricksConfigFromEnv() (databricksConfig, bool, error) {
	cfg := databricksConfig{
		Host:        strings.TrimSpace(os.Getenv(databricksHostEnv)),
		Token:       strings.TrimSpace(os.Getenv(databricksTokenEnv)),
		WarehouseID: strings.TrimSpace(os.Getenv(databricksWarehouseEnv)),
		Catalog:     strings.TrimSpace(os.Getenv(databricksCatalogEnv)),
		Schema:      strings.TrimSpace(os.Getenv(databricksSchemaEnv)),
	}
	values := map[string]string{
		databricksHostEnv:      cfg.Host,
		databricksTokenEnv:     cfg.Token,
		databricksWarehouseEnv: cfg.WarehouseID,
		databricksCatalogEnv:   cfg.Catalog,
		databricksSchemaEnv:    cfg.Schema,
	}
	setCount := 0
	var missing []string
	for name, value := range values {
		if value != "" {
			setCount++
			continue
		}
		missing = append(missing, name)
	}
	if setCount == 0 {
		return databricksConfig{}, false, nil
	}
	if len(missing) > 0 {
		return databricksConfig{}, false, fmt.Errorf("incomplete Databricks config; missing %s", strings.Join(missing, ", "))
	}
	if !validDatabricksIdentifier(cfg.Catalog) {
		return databricksConfig{}, false, fmt.Errorf("invalid Databricks catalog %q", cfg.Catalog)
	}
	if !validDatabricksIdentifier(cfg.Schema) {
		return databricksConfig{}, false, fmt.Errorf("invalid Databricks schema %q", cfg.Schema)
	}
	return cfg, true, nil
}

func validDatabricksIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// annotateDatabricksExport mirrors the event to Databricks when configured and
// records the outcome in the event metadata. Export never blocks or fails the
// hook: any error is surfaced as metadata only.
func (a *Agent) annotateDatabricksExport(event *protocol.EventJSON, payload cursorHookPayload) {
	exporter, err := a.checkpointExporter()
	if err != nil {
		setDatabricksMetadata(event, "config_error", "", err)
		return
	}
	if exporter == nil {
		return
	}

	checkpoint, err := buildDatabricksCheckpoint(event, payload)
	if err != nil {
		setDatabricksMetadata(event, "build_error", "", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tableName, err := exporter.ExportCheckpoint(ctx, checkpoint)
	if err != nil {
		setDatabricksMetadata(event, "export_error", "", err)
		return
	}
	setDatabricksMetadata(event, "ok", tableName, nil)
}

func buildDatabricksCheckpoint(event *protocol.EventJSON, payload cursorHookPayload) (DatabricksCheckpoint, error) {
	checkpoint := DatabricksCheckpoint{
		SessionID:           event.SessionID,
		RepoPath:            protocol.RepoRoot(),
		RepositorySystem:    "entire",
		AgentName:           AgentName,
		EventName:           firstNonEmpty(payload.HookEventName, "unknown"),
		EventType:           event.Type,
		Prompt:              firstNonEmpty(event.Prompt, payload.Prompt),
		SessionRef:          event.SessionRef,
		Model:               firstNonEmpty(event.Model, payload.Model),
		TranscriptAvailable: false,
		ModifiedFiles:       []string{},
		Metadata:            copyStringMap(event.Metadata),
		CapturedAt:          firstNonEmpty(event.Timestamp, time.Now().UTC().Format(time.RFC3339)),
	}

	branch, headCommit := repoState(checkpoint.RepoPath)
	checkpoint.Branch = branch
	checkpoint.HeadCommit = headCommit

	if event.SessionRef == "" {
		return checkpoint, nil
	}
	data, err := os.ReadFile(event.SessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return checkpoint, nil
		}
		return DatabricksCheckpoint{}, fmt.Errorf("read sidecar: %w", err)
	}
	records := parseSidecar(data)
	if len(records) == 0 {
		return checkpoint, nil
	}
	checkpoint.TranscriptAvailable = true
	checkpoint.ModifiedFiles = modifiedFilesFromRecords(records)
	if summary, ok := latestAssistantMessage(records); ok {
		checkpoint.Summary = summary
	}
	if checkpoint.Prompt == "" {
		checkpoint.Prompt = latestPrompt(records)
	}
	return checkpoint, nil
}

func latestAssistantMessage(records []sidecarRecord) (string, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if strings.TrimSpace(records[i].AssistantMessage) != "" {
			return records[i].AssistantMessage, true
		}
	}
	return "", false
}

func latestPrompt(records []sidecarRecord) string {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Event == HookNameBeforeSubmitPrompt && strings.TrimSpace(records[i].Prompt) != "" {
			return records[i].Prompt
		}
	}
	return ""
}

func repoState(repoRoot string) (string, string) {
	return runGit(repoRoot, "rev-parse", "--abbrev-ref", "HEAD"), runGit(repoRoot, "rev-parse", "HEAD")
}

func runGit(repoRoot string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func setDatabricksMetadata(event *protocol.EventJSON, state string, tableName string, err error) {
	if event.Metadata == nil {
		event.Metadata = map[string]string{}
	}
	event.Metadata["databricks_export"] = state
	if tableName != "" {
		event.Metadata["databricks_table"] = tableName
	}
	if err != nil {
		event.Metadata["databricks_error"] = truncateMetadataValue(err.Error())
	}
}

func truncateMetadataValue(value string) string {
	const maxLen = 240
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func (e *databricksExporter) ExportCheckpoint(ctx context.Context, checkpoint DatabricksCheckpoint) (string, error) {
	tableName := fullyQualifiedDatabricksTable(e.cfg)
	createTable := "CREATE TABLE IF NOT EXISTS " + tableName + " (" +
		"session_id STRING NOT NULL, " +
		"repo_path STRING NOT NULL, " +
		"repository_system STRING NOT NULL, " +
		"agent_name STRING NOT NULL, " +
		"event_name STRING NOT NULL, " +
		"event_type INT NOT NULL, " +
		"prompt STRING, " +
		"summary STRING, " +
		"session_ref STRING, " +
		"model STRING, " +
		"branch STRING, " +
		"head_commit STRING, " +
		"transcript_available BOOLEAN NOT NULL, " +
		"modified_files_json STRING NOT NULL, " +
		"metadata_json STRING NOT NULL, " +
		"captured_at STRING NOT NULL" +
		") USING DELTA"
	if err := e.executeStatement(ctx, createTable); err != nil {
		return tableName, err
	}

	modifiedFilesJSON, err := json.Marshal(checkpoint.ModifiedFiles)
	if err != nil {
		return tableName, fmt.Errorf("marshal modified files: %w", err)
	}
	metadataJSON, err := json.Marshal(checkpoint.Metadata)
	if err != nil {
		return tableName, fmt.Errorf("marshal metadata: %w", err)
	}
	insertStatement := fmt.Sprintf(
		"INSERT INTO %s (session_id, repo_path, repository_system, agent_name, event_name, event_type, prompt, summary, session_ref, model, branch, head_commit, transcript_available, modified_files_json, metadata_json, captured_at) VALUES (%s, %s, %s, %s, %s, %d, %s, %s, %s, %s, %s, %s, %t, %s, %s, %s)",
		tableName,
		sqlStringLiteral(checkpoint.SessionID),
		sqlStringLiteral(checkpoint.RepoPath),
		sqlStringLiteral(checkpoint.RepositorySystem),
		sqlStringLiteral(checkpoint.AgentName),
		sqlStringLiteral(checkpoint.EventName),
		checkpoint.EventType,
		sqlStringLiteral(checkpoint.Prompt),
		sqlStringLiteral(checkpoint.Summary),
		sqlStringLiteral(checkpoint.SessionRef),
		sqlStringLiteral(checkpoint.Model),
		sqlStringLiteral(checkpoint.Branch),
		sqlStringLiteral(checkpoint.HeadCommit),
		checkpoint.TranscriptAvailable,
		sqlStringLiteral(string(modifiedFilesJSON)),
		sqlStringLiteral(string(metadataJSON)),
		sqlStringLiteral(checkpoint.CapturedAt),
	)
	if err := e.executeStatement(ctx, insertStatement); err != nil {
		return tableName, err
	}
	return tableName, nil
}

func fullyQualifiedDatabricksTable(cfg databricksConfig) string {
	return backtickIdentifier(cfg.Catalog) + "." + backtickIdentifier(cfg.Schema) + "." + backtickIdentifier(databricksTableName)
}

func backtickIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (e *databricksExporter) executeStatement(ctx context.Context, statement string) error {
	payload, err := json.Marshal(statementExecutionRequest{
		WarehouseID: e.cfg.WarehouseID,
		Statement:   statement,
		WaitTimeout: "10s",
	})
	if err != nil {
		return fmt.Errorf("marshal Databricks statement: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.Host, "/")+databricksStatementRoute, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build Databricks request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute Databricks statement: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if readErr != nil {
			return fmt.Errorf("Databricks request failed with status %s", resp.Status)
		}
		return fmt.Errorf("Databricks request failed with status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if readErr != nil {
		return nil
	}

	var result statementExecutionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	switch result.Status.State {
	case "", "SUCCEEDED":
		return nil
	case "PENDING", "RUNNING":
		return fmt.Errorf("Databricks statement did not complete within wait_timeout")
	default:
		if strings.TrimSpace(result.Status.ErrorMessage) != "" {
			return fmt.Errorf("Databricks statement %s: %s", result.Status.State, strings.TrimSpace(result.Status.ErrorMessage))
		}
		return fmt.Errorf("Databricks statement %s", result.Status.State)
	}
}
