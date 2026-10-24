package storage

import (
	"database/sql"
	"errors"
	"os"
	"path"
	"strings"

	"rasterdock/internal/domain"
)

type publicationIntent struct {
	target, temp, identity, hash string
	size                         uint64
}

func (r *Receiver) loadIntent(f domain.File) (publicationIntent, error) {
	var intent publicationIntent
	err := r.db.QueryRow("SELECT target,temp,temp_identity,expected_size,expected_hash FROM publication WHERE file=?", f.ID).Scan(&intent.target, &intent.temp, &intent.identity, &intent.size, &intent.hash)
	if err != nil {
		return intent, err
	}
	if intent.target != f.Path || intent.size != f.SourceSize || intent.hash != f.SourceHash {
		return intent, ErrConflict
	}
	if intent.temp != "" {
		if !strings.HasPrefix(intent.temp, ".rasterdock-") || !strings.HasSuffix(intent.temp, ".tmp") || intent.identity == "" {
			return intent, ErrConflict
		}
		id := strings.TrimSuffix(strings.TrimPrefix(intent.temp, ".rasterdock-"), ".tmp")
		if _, err = domain.ParseID(id); err != nil {
			return intent, ErrConflict
		}
		// A user may legitimately transfer a name with our prefix. It is never scratch space.
		for _, file := range r.manifest.Files {
			if strings.EqualFold(file.Path, intent.temp) {
				return intent, ErrConflict
			}
		}
	}
	return intent, nil
}

// ownedTemp refuses symlinks and replaced inodes, including in-root links.
// Only a path from the pinned intent may be considered for deletion.
func ownedTemp(root *os.Root, intent publicationIntent) (*os.File, error) {
	info, err := root.Lstat(intent.temp)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrConflict
	}
	file, err := root.Open(intent.temp)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = ErrConflict
	}
	if err == nil {
		var identity string
		identity, err = fileIdentity(file)
		if err == nil && identity != intent.identity {
			err = ErrConflict
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (r *Receiver) cleanupTemp(root *os.Root, f domain.File, intent publicationIntent) error {
	if intent.temp == "" {
		r.published[f.ID] = true
		return nil
	}
	file, err := ownedTemp(root, intent)
	if err == nil {
		if err = file.Close(); err != nil {
			return err
		}
		if err = root.Remove(intent.temp); err != nil {
			return err
		}
		if err = r.fault("publication-temp-removed"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Even a previously removed name must be synced before forgetting the intent.
	if err = syncRootDir(root, "."); err != nil {
		return err
	}
	_, err = r.db.Exec("UPDATE publication SET temp='',temp_identity='' WHERE file=? AND temp=?", f.ID, intent.temp)
	return err
}

// recoverPublication reuses only a complete, verified temp. Missing/corrupt owned
// temps may be rebuilt from payload; unrelated paths are never swept by prefix.
func (r *Receiver) recoverPublication(root *os.Root, f domain.File) (bool, error) {
	intent, err := r.loadIntent(f)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	targetValid := checkFile(root, intent.target, f)
	if !targetValid {
		delete(r.published, f.ID)
		if _, err = r.db.Exec("UPDATE publication SET published=0 WHERE file=?", f.ID); err != nil {
			return false, err
		}
	}
	if intent.temp != "" {
		file, e := ownedTemp(root, intent)
		if e == nil {
			e = file.Close()
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return false, e
		}
	}
	if targetValid {
		if err = r.finishPublication(root, f); err != nil {
			return false, err
		}
		if err = r.cleanupTemp(root, f, intent); err != nil {
			return false, err
		}
		r.published[f.ID] = true
		return true, nil
	}
	if _, err = root.Lstat(intent.target); err == nil {
		return false, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if intent.temp == "" {
		return false, nil
	}
	if !checkFile(root, intent.temp, f) {
		if err = r.cleanupTemp(root, f, intent); err != nil {
			return false, err
		}
		delete(r.published, f.ID)
		return false, nil
	}
	if err = root.MkdirAll(path.Dir(intent.target), 0700); err != nil {
		return false, err
	}
	file, err := ownedTemp(root, intent)
	if err != nil {
		return false, err
	}
	err = errors.Join(Flush(file), file.Close())
	if err != nil {
		return false, err
	}
	if err = syncRootDir(root, "."); err != nil {
		return false, err
	}
	if err = r.linkPublication(root, f, intent); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Receiver) linkPublication(root *os.Root, f domain.File, intent publicationIntent) error {
	if err := r.fault("publication-flushed"); err != nil {
		return err
	}
	file, err := ownedTemp(root, intent)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = root.Link(intent.temp, intent.target); err != nil {
		return err
	}
	if err = r.fault("publication-linked"); err != nil {
		return err
	}
	if err = r.finishPublication(root, f); err != nil {
		return err
	}
	if err = r.cleanupTemp(root, f, intent); err != nil {
		return err
	}
	r.published[f.ID] = true
	return nil
}
