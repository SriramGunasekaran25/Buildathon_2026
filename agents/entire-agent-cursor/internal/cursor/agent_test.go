package cursor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/external-agents/agents/entire-agent-cursor/internal/protocol"
)

func testRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Setenv("ENTIRE_REPO_ROOT", repo)
	// Ensure Databricks stays disabled unless a test opts in.
	for _, key := range []string{
		databricksHostEnv, databricksTokenEnv, databricksWarehouseEnv,
		databricksCatalogEnv, databricksSchemaEnv,
	} {
		t.Setenv(key, "")
	}
	a := &Agent{}
	if dir, err := a.GetSessionDir(repo); err == nil {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return repo
}

func TestInfoDeclaresExpectedCapabilities(t *testing.T) {
	info := New().Info()
	if info.Name != "cursor" || info.Type != "Cursor Agent" {
		t.Fatalf("identity = %q/%q", info.Name, info.Type)
	}
	if info.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("protocol version = %d", info.ProtocolVersion)
	}
	caps := info.Capabilities
	if !caps.Hooks || !caps.TranscriptAnalyzer || !caps.CompactTranscript || !caps.UsesTerminal {
		t.Errorf("expected core capabilities enabled: %#v", caps)
	}
	if caps.TranscriptPreparer || caps.TokenCalculator || caps.TextGenerator ||
		caps.HookResponseWriter || caps.SubagentAwareExtractor {
		t.Errorf("unexpected extra capabilities enabled: %#v", caps)
	}
	if len(info.HookNames) != 4 {
		t.Errorf("hook names = %v", info.HookNames)
	}
	if len(info.ProtectedDirs) != 1 || info.ProtectedDirs[0] != ".cursor" {
		t.Errorf("protected dirs = %v", info.ProtectedDirs)
	}
}

func TestDetectUsesLookPath(t *testing.T) {
	present := &Agent{LookPath: func(string) (string, error) { return "/usr/bin/cursor-agent", nil }}
	if !present.Detect().Present {
		t.Error("expected present when binary resolves")
	}
	absent := &Agent{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	if absent.Detect().Present {
		t.Error("expected absent when binary is missing")
	}
}

func TestGetSessionID(t *testing.T) {
	a := New()
	if got := a.GetSessionID(&protocol.HookInputJSON{SessionID: "conv-9"}); got != "conv-9" {
		t.Errorf("session id = %q", got)
	}
	if got := a.GetSessionID(nil); got != stubSessionID {
		t.Errorf("nil input session id = %q", got)
	}
}

func TestGetSessionDirIsStableAndRepoScoped(t *testing.T) {
	a := New()
	dirA, err := a.GetSessionDir("/repo/one")
	if err != nil {
		t.Fatal(err)
	}
	dirA2, _ := a.GetSessionDir("/repo/one")
	dirB, _ := a.GetSessionDir("/repo/two")
	if dirA != dirA2 {
		t.Errorf("session dir not stable: %q vs %q", dirA, dirA2)
	}
	if dirA == dirB {
		t.Error("different repos must map to different dirs")
	}
	if !strings.Contains(dirA, "entire-cursor") {
		t.Errorf("session dir = %q", dirA)
	}
}

func TestResolveSessionFile(t *testing.T) {
	a := New()
	got := a.ResolveSessionFile("/tmp/x", "conv/../evil id")
	if filepath.Dir(got) != filepath.FromSlash("/tmp/x") {
		t.Errorf("dir = %q", filepath.Dir(got))
	}
	base := filepath.Base(got)
	if strings.ContainsAny(base, "/\\") || base == ".." || base == "." {
		t.Errorf("unsafe base = %q", base)
	}
}

func TestFormatResumeCommand(t *testing.T) {
	a := New()
	if got := a.FormatResumeCommand(""); got != "cursor-agent --continue" {
		t.Errorf("empty resume = %q", got)
	}
	if got := a.FormatResumeCommand("conv-1"); got != "cursor-agent --resume conv-1" {
		t.Errorf("resume = %q", got)
	}
	if got := a.FormatResumeCommand("a b"); !strings.Contains(got, "'a b'") {
		t.Errorf("unsafe id must be quoted: %q", got)
	}
}

func TestReadWriteSessionRoundTrip(t *testing.T) {
	testRepo(t)
	a := New()
	ref := filepath.Join(t.TempDir(), "conv-1.jsonl")
	native := []byte(`{"v":1,"agent":"cursor","event":"stop","session_id":"conv-1"}` + "\n")

	if err := a.WriteSession(protocol.AgentSessionJSON{SessionID: "conv-1", SessionRef: ref, NativeData: native}); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	session, err := a.ReadSession(&protocol.HookInputJSON{SessionID: "conv-1", SessionRef: ref})
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if session.AgentName != "cursor" || session.SessionID != "conv-1" {
		t.Errorf("session = %+v", session)
	}
	if session.NewFiles == nil || session.DeletedFiles == nil || session.ModifiedFiles == nil {
		t.Error("file slices must be initialized")
	}
}

func TestChunkAndReassemble(t *testing.T) {
	a := New()
	content := []byte("0123456789")
	if _, err := a.ChunkTranscript(content, 0); err == nil {
		t.Error("max-size 0 must error")
	}
	chunks, err := a.ChunkTranscript(content, 4)
	if err != nil {
		t.Fatalf("ChunkTranscript: %v", err)
	}
	if len(chunks) != 3 {
		t.Errorf("chunks = %d, want 3", len(chunks))
	}
	round, err := a.ReassembleTranscript(chunks)
	if err != nil || string(round) != string(content) {
		t.Errorf("round trip = %q, err = %v", round, err)
	}
}
