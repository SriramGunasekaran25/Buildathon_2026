package cursor

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
)

// hookEntry is a single Cursor hook action. It is modeled as a raw field map
// so foreign keys the user configured (env, timeout, matcher, ...) survive a
// read-modify-write cycle.
type hookEntry map[string]json.RawMessage

func (e hookEntry) command() string {
	raw, ok := e["command"]
	if !ok {
		return ""
	}
	var cmd string
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return ""
	}
	return cmd
}

func newEntireHook(command string) hookEntry {
	raw, _ := json.Marshal(command)
	return hookEntry{"command": raw}
}

// hooksFile mirrors .cursor/hooks.json. Unknown top-level keys are preserved
// through the extra map so Entire never clobbers unrelated configuration.
type hooksFile struct {
	Version int
	Hooks   map[string][]hookEntry
	extra   map[string]json.RawMessage
}

func (f *hooksFile) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.extra = map[string]json.RawMessage{}
	f.Hooks = map[string][]hookEntry{}
	for key, value := range raw {
		switch key {
		case "version":
			_ = json.Unmarshal(value, &f.Version)
		case "hooks":
			_ = json.Unmarshal(value, &f.Hooks)
			if f.Hooks == nil {
				f.Hooks = map[string][]hookEntry{}
			}
		default:
			f.extra[key] = value
		}
	}
	return nil
}

func (f hooksFile) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for key, value := range f.extra {
		out[key] = value
	}
	if f.Version != 0 {
		v, _ := json.Marshal(f.Version)
		out["version"] = v
	}
	hooks := f.Hooks
	if hooks == nil {
		hooks = map[string][]hookEntry{}
	}
	h, err := json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	out["hooks"] = h

	// Deterministic key order for stable file output.
	keys := make([]string, 0, len(out))
	for key := range out {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(out[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func readHooksFile(path string) hooksFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return hooksFile{}
	}
	var file hooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		return hooksFile{}
	}
	if file.Hooks == nil {
		file.Hooks = map[string][]hookEntry{}
	}
	return file
}

func hasEntireHook(entries []hookEntry, command string) bool {
	for _, entry := range entries {
		if entry.command() == command {
			return true
		}
	}
	return false
}

func upsertEntireHook(entries []hookEntry, command string) []hookEntry {
	if hasEntireHook(entries, command) {
		return entries
	}
	return append(entries, newEntireHook(command))
}

func removeEntireHook(entries []hookEntry, command string) []hookEntry {
	out := make([]hookEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.command() == command {
			continue
		}
		out = append(out, entry)
	}
	return out
}
