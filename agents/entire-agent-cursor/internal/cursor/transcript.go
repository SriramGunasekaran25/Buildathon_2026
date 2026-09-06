package cursor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// parseSidecar parses the append-only JSONL sidecar tolerantly: malformed
// lines are skipped so a partially written or rewound transcript still yields
// usable analysis instead of an error.
func parseSidecar(data []byte) []sidecarRecord {
	var records []sidecarRecord
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record sidecarRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records
}

func recordsFromOffset(records []sidecarRecord, offset int) []sidecarRecord {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(records) {
		return nil
	}
	return records[offset:]
}

// GetTranscriptPosition returns the sidecar record count. Positions are record
// indexes, not bytes, so appends advance the position by whole records.
func (a *Agent) GetTranscriptPosition(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return len(parseSidecar(data)), nil
}

func (a *Agent) ExtractModifiedFiles(path string, offset int) ([]string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, 0, nil
		}
		return nil, 0, err
	}
	records := parseSidecar(data)
	return modifiedFilesFromRecords(recordsFromOffset(records, offset)), len(records), nil
}

func (a *Agent) ExtractPrompts(sessionRef string, offset int) ([]string, error) {
	data, err := os.ReadFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	prompts := []string{}
	for _, record := range recordsFromOffset(parseSidecar(data), offset) {
		if record.Event != HookNameBeforeSubmitPrompt {
			continue
		}
		if strings.TrimSpace(record.Prompt) != "" {
			prompts = append(prompts, record.Prompt)
		}
	}
	return prompts, nil
}

// ExtractSummary returns the most recent assistant response captured on a stop
// event, which is what makes Cursor's answers visible in `entire explain`.
func (a *Agent) ExtractSummary(sessionRef string) (string, bool, error) {
	data, err := os.ReadFile(sessionRef)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	records := parseSidecar(data)
	for i := len(records) - 1; i >= 0; i-- {
		if strings.TrimSpace(records[i].AssistantMessage) != "" {
			return records[i].AssistantMessage, true, nil
		}
	}
	return "", false, nil
}

func modifiedFilesFromRecords(records []sidecarRecord) []string {
	files := []string{}
	seen := map[string]bool{}
	for _, record := range records {
		if record.Event != HookNameAfterFileEdit {
			continue
		}
		path := strings.TrimSpace(record.FilePath)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}

func modifiedFilesFromSidecar(data []byte, offset int) []string {
	if len(data) == 0 {
		return []string{}
	}
	return modifiedFilesFromRecords(recordsFromOffset(parseSidecar(data), offset))
}
