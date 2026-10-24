// Package config validates the currently supported concise job schema.
package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Job is the initial P1 raw-file profile. Unsupported options are rejected.
type Job struct {
	Source string `yaml:"source"`
}

// Settings contains application preferences, distinct from transfer jobs.
type Settings struct {
	Language string `yaml:"language"`
}

// LoadSettings reads only the central settings file and defaults to the OS language.
func LoadSettings(path string) (Settings, error) {
	s := Settings{Language: "system"}
	f, err := os.Open(path) //nolint:gosec // Explicit central settings pathname supplied by composition root.
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return s, err
	}
	if len(b) > 1<<20 {
		return s, errors.New("settings exceed 1 MiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var node yaml.Node
	if err = d.Decode(&node); err != nil {
		return s, err
	}
	if err = validateNode(&node); err != nil {
		return s, err
	}
	var extra yaml.Node
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return s, errors.New("expected one settings document")
	}
	d = yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err = d.Decode(&s); err != nil {
		return s, err
	}
	if s.Language != "system" && s.Language != "en" && s.Language != "ru" {
		return s, errors.New("unsupported language")
	}
	return s, nil
}

// ParseJob rejects unknown fields, duplicate keys, aliases and multiple documents.
func ParseJob(b []byte, location string) (Job, error) {
	var job Job
	if len(b) > 1<<20 {
		return job, errors.New("job exceeds 1 MiB")
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var node yaml.Node
	if err := d.Decode(&node); err != nil {
		return job, err
	}
	if err := validateNode(&node); err != nil {
		return job, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return job, errors.New("expected one YAML document")
	}
	d = yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&job); err != nil {
		return job, err
	}
	if job.Source == "" {
		return job, errors.New("source required")
	}
	if !filepath.IsAbs(job.Source) {
		job.Source = filepath.Join(filepath.Dir(location), job.Source)
	}
	return job, nil
}
func validateNode(n *yaml.Node) error {
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return errors.New("YAML aliases and anchors unsupported")
	}
	if n.Kind == yaml.ScalarNode && n.Tag != "!!str" {
		return errors.New("expected string YAML value")
	}
	for _, c := range n.Content {
		if err := validateNode(c); err != nil {
			return err
		}
	}
	return nil
}

// Root deliberately ignores platform config/XDG directories.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rasterdock"), nil
}
