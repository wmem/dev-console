package storage_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devconsole/internal/auth"
	"devconsole/internal/secretbox"
	"devconsole/internal/storage"
)

func TestVersionOneMigrationPreservesLegacyToken(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO schema_migrations VALUES(1); CREATE TABLE access_tokens(id TEXT PRIMARY KEY,data TEXT NOT NULL CHECK(json_valid(data)));"); err != nil {
		t.Fatal(err)
	}
	raw := "dc_" + auth.Random()
	id := auth.ID()
	oldJSON, _ := json.Marshal(map[string]any{"id": id, "project_id": "legacy-project", "name": "历史Token", "prefix": raw[:11], "digest": auth.Digest(raw), "scopes": []string{"docs:read"}, "enabled": true, "created_at": time.Now().UTC()})
	if _, err = db.Exec("INSERT INTO access_tokens VALUES(?,?)", id, string(oldJSON)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := storage.InspectVersion(dir); err != nil || version != 2 {
		t.Fatal("未迁移到格式2", version, err)
	}
	if _, err = os.Stat(filepath.Join(dir, secretbox.KeyFile)); err != nil {
		t.Fatal("迁移未准备加密密钥", err)
	}
	var currentJSON string
	if err = store.DB.QueryRow("SELECT data FROM access_tokens WHERE id=?", id).Scan(&currentJSON); err != nil || currentJSON != string(oldJSON) {
		t.Fatal("迁移改写了旧Token记录", err)
	}
	a := auth.New(store)
	if _, err = a.Bearer(raw); err != nil {
		t.Fatal("迁移后原Token失效", err)
	}
	meta, err := a.Tokens("legacy-project")
	if err != nil || len(meta) != 1 || meta[0].Recoverable {
		t.Fatal("旧Token恢复能力标记错误", err)
	}
	if _, err = a.ImportTokenSecret("legacy-project", id, raw); err != nil {
		t.Fatal("迁移后不能补存原文", err)
	}
	if _, restored, err := a.RevealToken("legacy-project", id); err != nil || restored != raw {
		t.Fatal("补存后的凭据无法恢复", err)
	}
}
