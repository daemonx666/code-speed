// Package sqlitedb opens another app's SQLite database for reading only,
// without ever creating or changing a file of it (pure Go, no cgo).
//
// An app in WAL mode that is not running has no -wal file; a read-only
// open would create one (or fail), so such a database is opened
// immutable, which reads the main file alone. Otherwise it is opened
// mode=ro with a short busy timeout, falling back to immutable when that
// open fails and no -wal file exists (Agent Speed, spec/METRICS.md §4.5).
package sqlitedb

import (
	"database/sql"
	"net/url"
	"os"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// isWAL reports whether the database header says WAL (bytes 18 and 19 are 2).
func isWAL(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 20)
	if n, _ := f.Read(b); n < 20 {
		return false
	}
	return b[18] == 2 && b[19] == 2
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// uriPath is path as a file: URI's path. A Windows path needs forward
// slashes and a leading one ("C:\\x\\y.db" → "/C:/x/y.db", so the URI
// is file:///C:/x/y.db); left as it is, SQLite can't open it.
func uriPath(path string, windows bool) string {
	if !windows {
		return path
	}
	path = strings.ReplaceAll(path, `\`, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func open(path, mode string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: uriPath(path, runtime.GOOS == "windows")}
	q := url.Values{}
	if mode == "immutable" {
		q.Set("immutable", "1")
	} else {
		q.Set("mode", "ro")
	}
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Open opens path for reading. immutable reports that the connection
// reads the main file alone: drop it when the file changes or a -wal file
// appears, and open again.
func Open(path string) (db *sql.DB, immutable bool, err error) {
	if !exists(path) {
		return nil, false, os.ErrNotExist
	}
	wal := exists(path + "-wal")
	if isWAL(path) && !wal {
		db, err = open(path, "immutable")
		return db, true, err
	}
	db, err = open(path, "ro")
	if err != nil && !wal {
		db, err = open(path, "immutable")
		return db, true, err
	}
	return db, false, err
}
