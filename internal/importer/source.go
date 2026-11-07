package importer

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// source is greengram-web's database, opened read-only. Tables and columns
// that later versions of the web server added are looked up, not assumed,
// so an older copy still imports (with less in it).
type source struct {
	db   *sql.DB
	cols map[string]map[string]bool
}

func openSource(ctx context.Context, path string) (*source, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// immutable: no locks and no -wal/-shm files, the copy stays byte for
	// byte what it was. It also means a WAL next to the file would be
	// ignored, so refuse one rather than import half the data.
	if fi, err := os.Stat(abs + "-wal"); err == nil && fi.Size() > 0 {
		return nil, fmt.Errorf("%s-wal is not empty; make the copy with sqlite3 .backup so the WAL is folded in", path)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro&immutable=1&_pragma=query_only(1)"}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path // file:/C:/... on Windows
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &source{db: db, cols: map[string]map[string]bool{}}
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()
	for _, t := range tables {
		s.cols[t] = map[string]bool{}
		r, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", t)
		if err != nil {
			db.Close()
			return nil, err
		}
		for r.Next() {
			var c string
			if err := r.Scan(&c); err != nil {
				r.Close()
				db.Close()
				return nil, err
			}
			s.cols[t][c] = true
		}
		r.Close()
	}
	for _, t := range []string{"users", "sessions", "chats", "members", "messages"} {
		if !s.has(t) {
			db.Close()
			return nil, fmt.Errorf("%s has no %s table; is it a greengram-web database?", path, t)
		}
	}
	return s, nil
}

func (s *source) Close() error { return s.db.Close() }

func (s *source) has(table string) bool { return s.cols[table] != nil }

func (s *source) hasCol(table, col string) bool { return s.cols[table][col] }

// col is table.col, or def where the column doesn't exist in this copy.
func (s *source) col(alias, table, col, def string) string {
	if s.hasCol(table, col) {
		return alias + "." + col
	}
	return def
}

// fingerprint names this copy of the database, so a resumed run can tell
// it is continuing with the same file.
func (s *source) fingerprint(ctx context.Context) (string, error) {
	var parts []string
	for _, t := range []string{"users", "chats", "messages", "members", "sessions"} {
		var n, max int64
		q := "SELECT count(*), coalesce(max(rowid), 0) FROM " + t
		if err := s.db.QueryRowContext(ctx, q).Scan(&n, &max); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s=%d/%d", t, n, max))
	}
	return strings.Join(parts, " "), nil
}
