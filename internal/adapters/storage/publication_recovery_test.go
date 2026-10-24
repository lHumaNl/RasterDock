package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

func publicationFixture(t *testing.T) (*application.Prepared, string, string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	dest := filepath.Join(base, "destination")
	for _, dir := range []string{src, dest} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), src, base, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(base, "rx")
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(0); n < p.PacketCount(); n++ {
		q, e := p.Packet(n)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Accept(q); e != nil {
			t.Fatal(e)
		}
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	return p, session, dest
}

func faultPublication(t *testing.T, p *application.Prepared, session, dest, stage string) {
	t.Helper()
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("publication fault")
	r.Fault = func(point string) error {
		if point == stage {
			return injected
		}
		return nil
	}
	if err = r.Publish(); !errors.Is(err, injected) {
		t.Fatalf("fault hook not reached: %v", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationReusesVerifiedTempWithoutPayload(t *testing.T) {
	p, session, dest := publicationFixture(t)
	faultPublication(t, p, session, dest, "publication-flushed")
	if err := os.Remove(filepath.Join(session, "payload", "1.bin")); err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}()
	files, _, err := r.PublishedFiles()
	if err != nil || files != 1 {
		t.Fatalf("verified temp not recovered: %d %v", files, err)
	}
}

func TestPublicationPreservesReplacedTemp(t *testing.T) {
	p, session, dest := publicationFixture(t)
	faultPublication(t, p, session, dest, "publication-flushed")
	names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
	if err != nil || len(names) != 1 {
		t.Fatal(names, err)
	}
	// Keep the original inode alive, so the replacement cannot reuse its identity.
	if err = os.Rename(names[0], filepath.Join(dest, "original-retained")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(names[0], []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if r != nil {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replaced temp not detected: %v", err)
	}
	b, err := os.ReadFile(names[0])
	if err != nil || string(b) != "unrelated" {
		t.Fatal("replacement changed", err)
	}
}

func TestPublicationAssemblyFailureTracksTemp(t *testing.T) {
	p, session, dest := publicationFixture(t)
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(session, "payload", "1.bin")
	if err = os.WriteFile(payload, []byte("corrupt!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.Publish(); !errors.Is(err, storage.ErrConflict) {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(payload, []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
	if err != nil || len(names) != 0 {
		t.Fatal("assembly failure leaked temp", names, err)
	}
}

func TestPublicationUntrackedTempIsNotDeleted(t *testing.T) {
	p, session, dest := publicationFixture(t)
	name := filepath.Join(dest, ".rasterdock-00000000-0000-4000-8000-000000000000.tmp")
	if err := os.WriteFile(name, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	faultPublication(t, p, session, dest, "publication-flushed")
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(name) //nolint:gosec // Test-owned temporary directory and fixed fixture name.
	if err != nil || string(b) != "unrelated" {
		t.Fatal("untracked file changed", err)
	}
}

func TestPublicationSymlinkTempRejected(t *testing.T) {
	p, session, dest := publicationFixture(t)
	faultPublication(t, p, session, dest, "publication-flushed")
	names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
	if err != nil || len(names) != 1 {
		t.Fatal(names, err)
	}
	retained := filepath.Join(dest, "retained")
	if err = os.Rename(names[0], retained); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("retained", names[0]); err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if r != nil {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if target, e := os.Readlink(names[0]); e != nil || target != "retained" {
		t.Fatal("link changed", e)
	}
}

func TestPublicationClearsStalePublishedBeforeTempConflict(t *testing.T) {
	p, session, dest := publicationFixture(t)
	faultPublication(t, p, session, dest, "publication-committed")
	names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
	if err != nil || len(names) != 1 {
		t.Fatal(names, err)
	}
	if err = os.Rename(names[0], filepath.Join(dest, "retained")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(names[0], []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dest, "a")); err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
	if r != nil {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(session, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var published int
	err = db.QueryRow("SELECT published FROM publication WHERE file=1").Scan(&published)
	err = errors.Join(err, db.Close())
	if err != nil || published != 0 {
		t.Fatalf("missing destination still marked published: %d %v", published, err)
	}
}

func TestPublicationRejectsCorruptIntent(t *testing.T) {
	for _, query := range []string{
		"UPDATE publication SET temp='../outside'",
		"UPDATE publication SET target='different'",
		"UPDATE publication SET expected_size=42",
		"UPDATE publication SET expected_hash='invalid'",
	} {
		t.Run(query, func(t *testing.T) {
			p, session, dest := publicationFixture(t)
			faultPublication(t, p, session, dest, "publication-flushed")
			db, err := sql.Open("sqlite3", filepath.Join(session, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(query)
			err = errors.Join(err, db.Close())
			if err != nil {
				t.Fatal(err)
			}
			r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
			if r != nil {
				if e := r.Close(); e != nil {
					t.Error(e)
				}
			}
			if !errors.Is(err, storage.ErrConflict) {
				t.Fatalf("corrupt intent accepted: %v", err)
			}
			names, e := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
			if e != nil || len(names) != 1 {
				t.Fatal("temp changed", names, e)
			}
		})
	}
}

func TestPublicationMissingOrCorruptTempRebuilt(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "corrupt"}[missing], func(t *testing.T) {
			p, session, dest := publicationFixture(t)
			faultPublication(t, p, session, dest, "publication-flushed")
			names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
			if err != nil || len(names) != 1 {
				t.Fatal(names, err)
			}
			if missing {
				err = os.Remove(names[0])
			} else {
				err = os.WriteFile(names[0], []byte("damaged!"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Publish(); err != nil {
				t.Fatal(err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			names, err = filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
			if err != nil || len(names) != 0 {
				t.Fatal(names, err)
			}
			b, err := os.ReadFile(filepath.Join(dest, "a")) //nolint:gosec // Test-owned temporary destination.
			if err != nil || string(b) != "verified" {
				t.Fatal("rebuilt file mismatch", err)
			}
		})
	}
}

func TestPublicationProcessCrash(t *testing.T) {
	for _, stage := range []string{"publication-intent", "publication-flushed", "publication-linked", "publication-directory", "publication-committed", "publication-temp-removed"} {
		t.Run(stage, func(t *testing.T) {
			p, session, dest := publicationFixture(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPublicationCrashChild$") //nolint:gosec // Re-exec only os.Executable(), the current test binary; no shell or external program.
			cmd.Env = append(os.Environ(), "RD_CRASH_STAGE="+stage, "RD_PREPARED="+p.Directory, "RD_SESSION="+session, "RD_DEST="+dest)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("child did not crash at requested boundary: %v %s", err, output)
			}
			r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Publish(); err != nil {
				t.Fatal(err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
			if err != nil || len(names) != 0 {
				t.Fatal("temp leak", names, err)
			}
			b, err := os.ReadFile(filepath.Join(dest, "a")) //nolint:gosec // Test-owned temporary destination.
			if err != nil || string(b) != "verified" {
				t.Fatal("publication mismatch", err)
			}
		})
	}
}

func TestPublicationCrashChild(t *testing.T) {
	stage := os.Getenv("RD_CRASH_STAGE")
	if stage == "" {
		return
	}
	p, err := application.OpenPrepared(os.Getenv("RD_PREPARED"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(os.Getenv("RD_SESSION"), os.Getenv("RD_DEST"), p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	r.Fault = func(point string) error {
		if point == stage {
			os.Exit(77)
		}
		return nil
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash hook not reached")
}
