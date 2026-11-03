package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"devconsole/internal/auth"
)

func TestAdminTokenRecoveryPermissionsAndRedaction(t *testing.T) {
	h := start(t, t.TempDir(), true)
	admin := h.login("admin", "test-password-only")
	p := admin.project("token-copy")
	pid := p["id"].(string)
	created := admin.do("POST", "/admin/projects/"+pid+"/tokens", map[string]any{"name": "可恢复"}, 201)
	raw := created["secret"].(string)
	id := created["token"].(map[string]any)["id"].(string)
	reveal := "/admin/projects/" + pid + "/tokens/" + id + "/reveal"
	for i := 0; i < 2; i++ {
		status, body, headers := admin.request("POST", reveal, map[string]any{})
		if status != 200 {
			t.Fatal("管理员取回失败", status)
		}
		var out struct {
			Secret string `json:"secret"`
		}
		json.Unmarshal(body, &out)
		if out.Secret != raw {
			t.Fatal("取回值与初次原文不同")
		}
		if headers.Get("Cache-Control") != "no-store" {
			t.Fatal("凭据响应可被缓存")
		}
	}
	status, list, _ := admin.request("GET", "/admin/projects/"+pid+"/tokens", nil)
	if status != 200 || strings.Contains(string(list), raw) || strings.Contains(string(list), "payload") || strings.Contains(string(list), "digest") {
		t.Fatal("普通列表泄漏凭据")
	}
	_, docs, _ := admin.request("GET", "/projects/token-copy/docs", nil)
	if strings.Contains(string(docs), raw) {
		t.Fatal("普通文档包含真实Token")
	}
	noCSRF := &client{h: h, cookie: admin.cookie}
	status, _, _ = noCSRF.request("POST", reveal, map[string]any{})
	if status != 403 {
		t.Fatal("凭据读取未检查CSRF", status)
	}
	bearer := &client{h: h, token: raw}
	status, _, _ = bearer.request("POST", reveal, map[string]any{})
	if status != 403 {
		t.Fatal("项目Token读到管理凭据", status)
	}
	user := admin.do("POST", "/admin/users", map[string]any{"username": "read-member", "password": "member-password", "role": "member"}, 201)
	admin.do("PUT", "/admin/projects/"+pid+"/members", map[string]any{"member_ids": []string{user["id"].(string)}, "revision": p["revision"]}, 200)
	member := h.login("read-member", "member-password")
	member.do("GET", "/projects/token-copy/docs", nil, 200)
	status, _, _ = member.request("POST", reveal, map[string]any{})
	if status != 403 {
		t.Fatal("普通用户读取管理凭据", status)
	}
	other := admin.project("other-token-project")
	status, _, _ = admin.request("POST", "/admin/projects/"+other["id"].(string)+"/tokens/"+id+"/reveal", map[string]any{})
	if status != 404 {
		t.Fatal("凭据项目不匹配仍返回", status)
	}
	legacyRaw := "dc_" + auth.Random()
	legacy := auth.Token{ID: auth.ID(), ProjectID: pid, Name: "历史凭据", Prefix: legacyRaw[:11], Digest: auth.Digest(legacyRaw), Scopes: []string{"docs:read"}, Enabled: true, CreatedAt: time.Now()}
	if err := h.store.Put("access_tokens", legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	legacyPath := "/admin/projects/" + pid + "/tokens/" + legacy.ID
	status, _, _ = admin.request("POST", legacyPath+"/reveal", map[string]any{})
	if status != 409 {
		t.Fatal("历史摘要被虚假还原", status)
	}
	status, _, _ = admin.request("PUT", legacyPath+"/secret", map[string]any{"secret": "错误原文"})
	if status != 400 {
		t.Fatal("错误原文补存未拒绝", status)
	}
	status, _, _ = admin.request("PUT", legacyPath+"/secret", map[string]any{"secret": legacyRaw})
	if status != 200 {
		t.Fatal("历史原文不能补存", status)
	}
	status, body, _ := admin.request("POST", legacyPath+"/reveal", map[string]any{})
	if status != 200 || !strings.Contains(string(body), legacyRaw) {
		t.Fatal("历史原文补存后不能恢复", status)
	}
	admin.do("DELETE", "/admin/projects/"+pid+"/tokens/"+id, nil, 200)
	status, _, _ = admin.request("POST", reveal, map[string]any{})
	if status != 409 {
		t.Fatal("已撤销Token仍可取回", status)
	}
	expiring, _, err := h.auth.NewToken(pid, "过期验证", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var expired auth.Token
	if err = h.store.Get("access_tokens", expiring.ID, &expired); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	expired.ExpiresAt = &past
	if err = h.store.Put("access_tokens", expired.ID, expired); err != nil {
		t.Fatal(err)
	}
	status, _, _ = admin.request("POST", "/admin/projects/"+pid+"/tokens/"+expiring.ID+"/reveal", map[string]any{})
	if status != 409 {
		t.Fatal("过期Token仍可取回", status)
	}
}
