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
	if version, err := storage.InspectVersion(dir); err != nil || version != storage.CurrentVersion {
		t.Fatal("未迁移到当前格式", version, err)
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

func TestVersionTwoMigrationPreservesEncryptedPayloadAndKey(t *testing.T) {
	dir := t.TempDir()
	box, err := secretbox.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := box.Seal("fixture-recoverable-token", "fixture-aad")
	keyPath := filepath.Join(dir, secretbox.KeyFile)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO schema_migrations VALUES(2); CREATE TABLE token_secrets(id TEXT PRIMARY KEY,data TEXT NOT NULL CHECK(json_valid(data))); INSERT INTO token_secrets VALUES('preserved',json_object('version',1,'payload',?));", ciphertext); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var payload string
	if err := s.DB.QueryRow("SELECT json_extract(data,'$.payload') FROM token_secrets WHERE id='preserved'").Scan(&payload); err != nil || payload != ciphertext {
		t.Fatal("格式2密文被改写", err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("迁移重建了密钥")
	}
	if plaintext, err := s.Secrets.Open(payload, "fixture-aad"); err != nil || plaintext != "fixture-recoverable-token" {
		t.Fatal("迁移后的旧密文无法解密", err)
	}
	if _, err := s.DB.Exec("SELECT COUNT(*) FROM xiaomi_credentials"); err != nil {
		t.Fatal("未创建米家凭据表", err)
	}
}
