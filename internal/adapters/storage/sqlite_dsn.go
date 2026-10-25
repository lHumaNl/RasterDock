package storage

import (
	"net/url"
	"path/filepath"
	"strings"
)

const receiveSQLiteOptions = "_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_cache_size=-2048"
const discoverySQLiteOptions = "mode=ro&_query_only=1"

func sqliteDSN(path, options string) string {
	volume := filepath.VolumeName(path)
	path = filepath.ToSlash(path)
	// SQLite requires /C:/ rather than C:\; otherwise URL.String emits an authority.
	if len(volume) == 2 && volume[1] == ':' {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: options, OmitHost: !strings.HasPrefix(path, "/")}
	return uri.String()
}
