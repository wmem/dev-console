package secretbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthenticatedEncryptionAndKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	box, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, KeyFile)
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		t.Fatal("密钥未完整创建", err)
	}
	if st, err := os.Stat(keyPath); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("新密钥默认权限不正确", err)
	}
	ciphertext := box.Seal("测试凭据", "项目与Token身份")
	other := box.Seal("测试凭据", "项目与Token身份")
	if ciphertext == other {
		t.Fatal("重复加密未使用随机nonce")
	}
	reloaded, err := Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := reloaded.Open(ciphertext, "项目与Token身份")
	if err != nil || raw != "测试凭据" {
		t.Fatal("重启后不能还原凭据", err)
	}
	if _, err = reloaded.Open(ciphertext, "其他项目身份"); err == nil {
		t.Fatal("AAD变化未拒绝")
	}
	bad := []byte(ciphertext)
	bad[len(bad)/2] ^= 1
	if _, err = reloaded.Open(string(bad), "项目与Token身份"); err == nil {
		t.Fatal("密文损坏未拒绝")
	}
	wrong, err := Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Open(ciphertext, "项目与Token身份"); err == nil {
		t.Fatal("其他密钥错误还原了凭据")
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, true); err == nil {
		t.Fatal("已有密文时自动创建了替换密钥")
	}
	if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("缺失密钥被悄悄重新生成")
	}
	if err = os.WriteFile(keyPath, []byte("损坏的密钥"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, false); err == nil {
		t.Fatal("格式损坏密钥未拒绝")
	}
}
