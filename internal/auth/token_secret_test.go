package auth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devconsole/internal/secretbox"
	"devconsole/internal/storage"
)

func TestRecoverableTokensAndLegacyImport(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	a := New(store)
	token, raw, err := a.NewToken("project-a", "新Token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !token.Recoverable || token.Digest != "" {
		t.Fatal("公开Token元数据不正确")
	}
	var cipherJSON string
	if err = store.DB.QueryRow("SELECT data FROM token_secrets WHERE id=?", token.ID).Scan(&cipherJSON); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(cipherJSON), []byte(raw)) {
		t.Fatal("数据库保存了Token明文")
	}
	listed, err := a.Tokens("project-a")
	if err != nil || len(listed) != 1 || !listed[0].Recoverable || listed[0].Digest != "" {
		t.Fatal("Token列表不正确", err)
	}
	listedJSON, _ := json.Marshal(listed)
	if bytes.Contains(listedJSON, []byte(raw)) || bytes.Contains(listedJSON, []byte("payload")) {
		t.Fatal("列表泄漏凭据")
	}
	for i := 0; i < 2; i++ {
		_, revealed, err := a.RevealToken("project-a", token.ID)
		if err != nil || revealed != raw {
			t.Fatal("不能重复取回原Token", err)
		}
	}
	if _, _, err = a.RevealToken("project-b", token.ID); err == nil {
		t.Fatal("跨项目读取凭据未拒绝")
	}
	legacyRaw := "dc_" + Random()
	legacy := Token{ID: ID(), ProjectID: "project-a", Name: "旧Token", Prefix: legacyRaw[:11], Digest: Digest(legacyRaw), Enabled: true, Scopes: []string{"docs:read"}, CreatedAt: time.Now()}
	if err = store.Put("access_tokens", legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Bearer(legacyRaw); err != nil {
		t.Fatal("旧摘要Token不能继续鉴权", err)
	}
	if _, _, err = a.RevealToken("project-a", legacy.ID); err == nil {
		t.Fatal("旧Token被错误声称可恢复")
	}
	if _, err = a.ImportTokenSecret("project-a", legacy.ID, "不匹配原文"); err == nil {
		t.Fatal("错误旧Token原文被接受")
	}
	if _, err = a.ImportTokenSecret("project-a", legacy.ID, legacyRaw); err != nil {
		t.Fatal(err)
	}
	if _, revealed, err := a.RevealToken("project-a", legacy.ID); err != nil || revealed != legacyRaw {
		t.Fatal("旧Token补存失败", err)
	}
	// 替换密文到另一Token记录不能绕过身份绑定。
	var encrypted encryptedToken
	if err = store.Get("token_secrets", token.ID, &encrypted); err != nil {
		t.Fatal(err)
	}
	if err = store.Put("token_secrets", legacy.ID, encrypted); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.RevealToken("project-a", legacy.ID); err == nil {
		t.Fatal("密文跨Token替换未拒绝")
	}
	store.Close()
	store, err = storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a = New(store)
	if _, revealed, err := a.RevealToken("project-a", token.ID); err != nil || revealed != raw {
		t.Fatal("持久化重启后凭据丢失", err)
	}
	if err = a.Revoke("project-a", token.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.RevealToken("project-a", token.ID); err == nil {
		t.Fatal("撤销后仍可取回")
	}
	if _, err = a.Bearer(raw); err == nil {
		t.Fatal("撤销后仍可鉴权")
	}
	// 保留另一条密文，密钥缺失时不能悄悄创建新密钥。
	store.Close()
	if err = os.Remove(filepath.Join(dir, secretbox.KeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Open(dir); err == nil {
		t.Fatal("密钥丢失时仍打开了含密文的数据库")
	}
}
