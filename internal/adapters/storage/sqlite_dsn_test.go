package storage

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSQLiteDSNDiscoveryEscapedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space # %23 & café.sqlite")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// An empty database reaches schema validation only if the URI opens correctly.
	_, err := queryReceiveSummary(context.Background(), path, "")
	if err == nil || err.Error() != "unsupported receive schema" {
		t.Fatalf("expected schema validation, got %v", err)
	}
}

func TestSQLiteDSNPathEncoding(t *testing.T) {
	paths := []string{"/tmp/space # %23 ? & café.sqlite"}
	if runtime.GOOS == "windows" {
		paths = append(paths, `C:\Users\space # %23 & café\state.sqlite`, `C:/Users/state.sqlite`, `\\server\share\state.sqlite`)
	} else {
		paths = append(paths, `/tmp/literal\backslash.sqlite`)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) { assertSQLiteURI(t, path) })
	}
}

func TestSQLiteDSNRelativePath(t *testing.T) {
	want := "file:relative%20%23%20%2523%20%3F.sqlite?" + discoverySQLiteOptions
	if got := sqliteDSN("relative # %23 ?.sqlite", discoverySQLiteOptions); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func assertSQLiteURI(t *testing.T, path string) {
	t.Helper()
	u, err := url.Parse(sqliteDSN(path, discoverySQLiteOptions))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(path)
	if volume := filepath.VolumeName(path); len(volume) == 2 && volume[1] == ':' {
		want = "/" + want
	}
	if u.Scheme != "file" || u.Host != "" || u.Path != want || u.Fragment != "" || u.RawQuery != discoverySQLiteOptions {
		t.Fatalf("URI did not preserve path/options: %+v; want path %q", u, want)
	}
}

func TestSQLiteDSNReceiveOptionsAndEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space # %23 & café.sqlite")
	db := openSQLiteDSNTest(t, path, receiveSQLiteOptions)
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000", "cache_size": "-2048"} {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %q, want %q; error %v", pragma, got, want, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not created at literal path: %v", err)
	}
	assertSQLiteReadOnly(t, path)
}

func openSQLiteDSNTest(t *testing.T, path, options string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", sqliteDSN(path, options))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertSQLiteReadOnly(t *testing.T, path string) {
	t.Helper()
	db := openSQLiteDSNTest(t, path, discoverySQLiteOptions)
	var queryOnly int
	if err := db.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		t.Fatalf("query_only = %d, error %v", queryOnly, err)
	}
	// Disable the driver guard to verify SQLite's mode=ro independently.
	if _, err := db.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE forbidden(id INTEGER)"); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("expected read-only rejection, got %v", err)
	}
}
