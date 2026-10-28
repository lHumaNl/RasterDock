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
