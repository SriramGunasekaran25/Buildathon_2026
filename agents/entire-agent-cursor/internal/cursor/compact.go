package cursor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/entireio/external-agents/agents/entire-agent-cursor/internal/protocol"
)

type compactRecord struct {
	V       int              `json:"v"`
	Agent   string           `json:"agent"`
	Type    string           `json:"type"`
	TS      string           `json:"ts,omitempty"`
	Content []compactContent `json:"content"`
}

type compactContent struct {
	Type  string         `json:"type,omitempty"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// CompactTranscript renders the sidecar into Entire v1 compact JSONL wrapped in
// base64. Prompts become user turns, file edits become assistant tool_use
// blocks, and stop responses become assistant text.
func (a *Agent) CompactTranscript(sessionRef string) (protocol.CompactTranscriptResponse, error) {
	data, err := os.ReadFile(sessionRef)
	if err != nil {
		return protocol.CompactTranscriptResponse{}, err
	}
	records := parseSidecar(data)
	if len(records) == 0 {
		return protocol.CompactTranscriptResponse{}, errors.New("empty transcript")
	}

	var lines [][]byte
	for _, record := range records {
		var compact *compactRecord
		switch record.Event {
		case HookNameBeforeSubmitPrompt:
			if strings.TrimSpace(record.Prompt) == "" {
				continue
			}
			compact = &compactRecord{
				V: sidecarVersion, Agent: AgentName, Type: "user", TS: record.TS,
				Content: []compactContent{{Text: record.Prompt}},
			}
		case HookNameAfterFileEdit:
			if strings.TrimSpace(record.FilePath) == "" {
				continue
			}
			compact = &compactRecord{
				V: sidecarVersion, Agent: AgentName, Type: "assistant", TS: record.TS,
				Content: []compactContent{{
					Type:  "tool_use",
					Name:  "edit",
					Input: map[string]any{"path": record.FilePath},
				}},
			}
		case HookNameStop:
			if strings.TrimSpace(record.AssistantMessage) == "" {
				continue
			}
			compact = &compactRecord{
				V: sidecarVersion, Agent: AgentName, Type: "assistant", TS: record.TS,
				Content: []compactContent{{Text: record.AssistantMessage}},
			}
		default:
			continue
		}
		encoded, err := json.Marshal(compact)
		if err != nil {
			return protocol.CompactTranscriptResponse{}, err
		}
		lines = append(lines, encoded)
	}

	if len(lines) == 0 {
		return protocol.CompactTranscriptResponse{}, errors.New("transcript has no compactable content")
	}

	joined := append([]byte(nil), lines[0]...)
	for _, line := range lines[1:] {
		joined = append(joined, '\n')
		joined = append(joined, line...)
	}
	return protocol.CompactTranscriptResponse{
		Transcript: base64.StdEncoding.EncodeToString(joined),
	}, nil
}
