package application

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// On Windows state is the caller-established namespace anchor, not a claim
// about its parent binding. It must exist, be private, and remain stable during
// Prepare. Readiness flushes every managed descendant and state itself.
func makePreparedDirectory(dir, state string) error {
	if runtime.GOOS != "windows" {
		return os.MkdirAll(dir, 0700)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(state, dir)
	if err == nil {
		err = root.MkdirAll(rel, 0700)
	}
	return errors.Join(err, root.Close())
}

func preparationAnchor(state string, anchors []string) (string, error) {
	if runtime.GOOS != "windows" || len(anchors) == 0 {
		return state, nil
	}
	if len(anchors) != 1 || anchors[0] == "" {
		return "", errors.New("preparation requires one containing namespace anchor")
	}
	anchor, err := filepath.Abs(anchors[0])
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(anchor, state)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("preparation state escapes namespace anchor")
	}
	return anchor, nil
}

func atPreparationBoundary(name, boundary string) bool {
	if runtime.GOOS != "windows" {
		return name == filepath.Clean(boundary)
	}
	rel, err := filepath.Rel(boundary, name)
	return err == nil && rel == "."
}
