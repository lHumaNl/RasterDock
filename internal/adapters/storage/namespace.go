package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// ErrNamespace requires an explicit, existing, caller-established namespace.
// Its binding in its parent must already be established by the caller;
// existence alone is NOT proof of durability. Every directory from payload
// through anchor is flushed on every attempt, never unrelated ancestors.
// The caller must prevent concurrent renames/reparse-point changes in state.
var ErrNamespace = errors.New("receive namespace requires one existing containing anchor")

// Validate the session itself: owner.lock and state.sqlite are siblings of
// payload and must never be created outside the declared managed namespace.
func initializeNamespace(session string, syncDirectory func(string) error, anchors []string) error {
	name := filepath.Join(session, "payload")
	if len(anchors) == 0 && runtime.GOOS != "windows" {
		return mkdirAllDurable(name, syncDirectory)
	}
	if len(anchors) != 1 || anchors[0] == "" {
		return ErrNamespace
	}
	anchor, err := filepath.Abs(anchors[0])
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(anchor, session)
	if err != nil || !filepath.IsLocal(rel) {
		return errors.Join(ErrNamespace, err)
	}
	return initializeAnchored(name, anchor, syncDirectory)
}

func initializeAnchored(name, anchor string, syncDirectory func(string) error) error {
	rel, err := filepath.Rel(anchor, name)
	if err != nil || !filepath.IsLocal(rel) {
		return errors.Join(ErrNamespace, err)
	}
	if err = makeNamespaceDirectories(anchor, rel); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return mkdirAllDurable(name, syncDirectory)
	}
	// Rebase spelling on the anchor: Windows paths are case-insensitive, while
	// the loop's boundary comparison must be exact and guaranteed to terminate.
	return syncManagedChain(filepath.Join(anchor, rel), anchor, syncDirectory)
}

func makeNamespaceDirectories(anchor, relative string) error {
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return err
	}
	return errors.Join(root.MkdirAll(relative, 0700), root.Close())
}

func syncManagedChain(name, anchor string, syncDirectory func(string) error) error {
	for current := name; ; current = filepath.Dir(current) {
		if err := syncDirectory(current); err != nil {
			return err
		}
		if current == anchor {
			return nil
		}
	}
}
