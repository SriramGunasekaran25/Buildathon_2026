package cursor

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sidecarFixture = `{"v":1,"agent":"cursor","event":"before-submit-prompt","session_id":"conv-123","ts":"2026-09-06T05:00:00Z","prompt":"Create hello.txt","model":"cursor-fast"}
{"v":1,"agent":"cursor","event":"after-file-edit","session_id":"conv-123","ts":"2026-09-06T05:00:02Z","file_path":"/repo/hello.txt"}
{"v":1,"agent":"cursor","event":"after-file-edit","session_id":"conv-123","ts":"2026-09-06T05:00:03Z","file_path":"/repo/hello.txt"}
{"v":1,"agent":"cursor","event":"stop","session_id":"conv-123","ts":"2026-09-06T05:00:05Z","assistant_message":"Created hello.txt"}
`

func writeSidecarFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conv-123.jsonl")
	if err := os.WriteFile(path, []byte(sidecarFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGetTranscriptPosition(t *testing.T) {
	a := New()
	path := writeSidecarFixture(t)
	pos, err := a.GetTranscriptPosition(path)
	if err != nil {
		t.Fatalf("GetTranscriptPosition: %v", err)
	}
	if pos != 4 {
		t.Errorf("position = %d, want 4", pos)
	}
	missing, err := a.GetTranscriptPosition(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || missing != 0 {
		t.Errorf("missing = (%d, %v), want (0, nil)", missing, err)
	}
}

func TestExtractModifiedFiles(t *testing.T) {
	a := New()
	path := writeSidecarFixture(t)
	files, pos, err := a.ExtractModifiedFiles(path, 0)
	if err != nil {
		t.Fatalf("ExtractModifiedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "/repo/hello.txt" {
		t.Errorf("files = %v (duplicates must be de-duped)", files)
	}
	if pos != 4 {
		t.Errorf("position = %d, want 4", pos)
	}
	// Offset past the edits sees nothing.
	files, _, err = a.ExtractModifiedFiles(path, 3)
	if err != nil || len(files) != 0 {
		t.Errorf("offset 3: files = %v err = %v", files, err)
	}
}

func TestExtractPrompts(t *testing.T) {
	a := New()
	path := writeSidecarFixture(t)
	prompts, err := a.ExtractPrompts(path, 0)
	if err != nil {
		t.Fatalf("ExtractPrompts: %v", err)
	}
	if len(prompts) != 1 || prompts[0] != "Create hello.txt" {
		t.Errorf("prompts = %v", prompts)
	}
}

func TestExtractSummary(t *testing.T) {
	a := New()
	path := writeSidecarFixture(t)
	summary, ok, err := a.ExtractSummary(path)
	if err != nil {
		t.Fatalf("ExtractSummary: %v", err)
	}
	if !ok || summary != "Created hello.txt" {
		t.Errorf("summary = (%q, %v)", summary, ok)
	}
}

func TestCompactTranscript(t *testing.T) {
	a := New()
	path := writeSidecarFixture(t)
	resp, err := a.CompactTranscript(path)
	if err != nil {
		t.Fatalf("CompactTranscript: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Transcript)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	// user prompt + 2 file edits + stop text = 4 compact lines.
	if len(lines) != 4 {
		t.Fatalf("compact lines = %d, want 4:\n%s", len(lines), raw)
	}
	if !strings.Contains(lines[0], `"user"`) || !strings.Contains(lines[0], "Create hello.txt") {
		t.Errorf("first line = %s", lines[0])
	}
	if !strings.Contains(lines[1], `"tool_use"`) || !strings.Contains(lines[1], "/repo/hello.txt") {
		t.Errorf("tool line = %s", lines[1])
	}
	if !strings.Contains(lines[3], `"assistant"`) || !strings.Contains(lines[3], "Created hello.txt") {
		t.Errorf("assistant line = %s", lines[3])
	}
}

func TestCompactTranscriptRejectsEmpty(t *testing.T) {
	a := New()
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompactTranscript(empty); err == nil {
		t.Error("empty transcript must error")
	}
}
