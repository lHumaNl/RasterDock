package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"go.yaml.in/yaml/v3"

	"rasterdock/internal/adapters/filesystem"
)

// Sender preference bounds are shared by storage and the GUI.
const (
	DefaultTimerCycles    uint64 = 3
	MaxTimerCycles        uint64 = 1000000
	MaxIdleTimeoutSeconds uint64 = 86400
)

// Settings contains application preferences, distinct from transfer jobs.
type Settings struct {
	Language           string `yaml:"language"`
	TimerCycles        uint64 `yaml:"timer_cycles"`
	IdleTimeoutSeconds uint64 `yaml:"idle_timeout_seconds"`
}

// DefaultSettings also applies to fields absent in older settings files.
func DefaultSettings() Settings { return Settings{Language: "system", TimerCycles: DefaultTimerCycles} }

// LoadSettings reads the central preferences; only a missing file uses defaults.
func LoadSettings(path string) (Settings, error) {
	s, _, err := readSettings(path)
	return s, err
}

func readSettings(path string) (Settings, *yaml.Node, error) {
	s := DefaultSettings()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, nil
	}
	if err != nil {
		return s, nil, err
	}
	if !info.Mode().IsRegular() {
		return s, nil, errors.New("settings must be a regular file, not a symlink")
	}
	f, err := os.Open(path) //nolint:gosec // Explicit settings pathname from the composition root.
	if err != nil {
		return s, nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return s, nil, err
	}
	if !os.SameFile(info, opened) {
		return s, nil, errors.New("settings changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return s, nil, err
	}
	if len(b) > 1<<20 {
		return s, nil, errors.New("settings exceed 1 MiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var node, extra yaml.Node
	if err = d.Decode(&node); err != nil {
		return s, nil, err
	}
	if err = validateSettingsNode(&node); err != nil {
		return s, nil, err
	}
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return s, nil, errors.New("expected one settings document")
	}
	if err = node.Decode(&s); err != nil {
		return s, nil, err
	}
	if s.Language != "system" && s.Language != "en" && s.Language != "ru" {
		return s, nil, errors.New("unsupported language")
	}
	return s, &node, validateSenderSettings(s.TimerCycles, s.IdleTimeoutSeconds)
}

// Deliberately separate from the string-only transfer job schema.
func validateSettingsNode(node *yaml.Node) error {
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 {
		return errors.New("expected settings mapping")
	}
	m := node.Content[0]
	if m.Kind != yaml.MappingNode || m.Anchor != "" || m.Tag != "!!map" {
		return errors.New("expected unanchored settings mapping")
	}
	seen := make(map[string]bool)
	for i := 0; i < len(m.Content); i += 2 {
		key, value := m.Content[i], m.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || seen[key.Value] {
			return errors.New("invalid or duplicate settings key")
		}
		seen[key.Value] = true
		tag := "!!int"
		switch key.Value {
		case "language":
			tag = "!!str"
		case "timer_cycles", "idle_timeout_seconds":
		default:
			return fmt.Errorf("unknown setting %q", key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Tag != tag || value.Anchor != "" {
			return fmt.Errorf("invalid value for setting %q", key.Value)
		}
	}
	return nil
}

func validateSenderSettings(cycles, idle uint64) error {
	if cycles == 0 || cycles > MaxTimerCycles {
		return errors.New("timer cycles must be between 1 and 1000000")
	}
	if idle > MaxIdleTimeoutSeconds {
		return errors.New("idle timeout seconds must be between 0 and 86400")
	}
	return nil
}

var settingsWriteMu sync.Mutex

// SaveSenderSettings rereads/validates the file, preserving language and comments.
// Writes are serialized in-process; separate application instances are last-writer-wins.
// A synced same-directory temporary file replaces the old file without truncating it.
func SaveSenderSettings(path string, cycles, idle uint64) error {
	if err := validateSenderSettings(cycles, idle); err != nil {
		return err
	}
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	_, node, err := readSettings(path)
	if err != nil {
		return err
	}
	setSettingInteger(node.Content[0], "timer_cycles", cycles)
	setSettingInteger(node.Content[0], "idle_timeout_seconds", idle)
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err = encoder.Encode(node); err != nil {
		return err
	}
	if err = encoder.Close(); err != nil {
		return err
	}
	if b.Len() > 1<<20 {
		return errors.New("settings exceed 1 MiB")
	}
	return replaceSettings(path, b.Bytes())
}

func setSettingInteger(m *yaml.Node, key string, value uint64) {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Value = strconv.FormatUint(value, 10)
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(value, 10)})
}

func replaceSettings(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = filesystem.Sync(f); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return filesystem.SyncDir(dir)
}
