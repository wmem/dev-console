package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"

	"devconsole/internal/secretbox"
)

const MinVersion = 1
const CurrentVersion = 2

// InspectVersion只读检查已有数据库，不建目录、不迁移，供升级与回退判断兼容性。
func InspectVersion(dir string) (int, error) {
	p, err := filepath.Abs(filepath.Join(dir, "platform.db"))
	if err != nil {
		return 0, err
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

var tables = []string{"users", "sessions", "projects", "project_members", "devices", "device_services", "project_services", "access_tokens", "token_secrets", "access_configs", "invocations"}

type Store struct {
	DB      *sql.DB
	Secrets *secretbox.Box
}
type Change struct {
	Table, ID string
	Value     any
	Delete    bool
}

func Open(dir string) (*Store, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "platform.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if _, e = db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY);"); e != nil {
		db.Close()
		return nil, e
	}
	var version int
	if e = db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&version); e != nil || version > CurrentVersion {
		db.Close()
		return nil, fmt.Errorf("数据库版本不受当前程序支持：%d", version)
	}
	tx, e := db.Begin()
	if e != nil {
		db.Close()
		return nil, e
	}
	for _, t := range tables {
		if _, e = tx.Exec("CREATE TABLE IF NOT EXISTS " + t + " (id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)))"); e != nil {
			tx.Rollback()
			db.Close()
			return nil, e
		}
	}
	var encrypted int
	if e = tx.QueryRow("SELECT COUNT(*) FROM token_secrets").Scan(&encrypted); e != nil {
		tx.Rollback()
		db.Close()
		return nil, e
	}
	if s.Secrets, e = secretbox.Open(dir, encrypted > 0); e != nil {
		tx.Rollback()
		db.Close()
		return nil, e
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO schema_migrations VALUES(?)", CurrentVersion); e != nil {
		tx.Rollback()
		db.Close()
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func valid(t string) bool {
	for _, x := range tables {
		if x == t {
			return true
		}
	}
	return false
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Get(t, id string, v any) error {
	if !valid(t) {
		return fmt.Errorf("未知数据表")
	}
	var b []byte
	if e := s.DB.QueryRow("SELECT data FROM "+t+" WHERE id=?", id).Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func List[T any](s *Store, t string) ([]T, error) {
	if !valid(t) {
		return nil, fmt.Errorf("未知数据表")
	}
	rows, e := s.DB.Query("SELECT data FROM " + t + " ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var v T
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Put(t, id string, v any) error { return s.Batch(Change{Table: t, ID: id, Value: v}) }
func (s *Store) Delete(t, id string) error     { return s.Batch(Change{Table: t, ID: id, Delete: true}) }
func (s *Store) Batch(changes ...Change) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, c := range changes {
		if !valid(c.Table) {
			return fmt.Errorf("未知数据表")
		}
		if c.Delete {
			_, e = tx.Exec("DELETE FROM "+c.Table+" WHERE id=?", c.ID)
		} else {
			var b []byte
			b, e = json.Marshal(c.Value)
			if e == nil {
				_, e = tx.Exec("INSERT INTO "+c.Table+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", c.ID, string(b))
			}
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
