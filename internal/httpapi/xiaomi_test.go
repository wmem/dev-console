package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"devconsole/internal/access/router"
	"devconsole/internal/access/xiaomi"
	"devconsole/internal/httpapi"
	"devconsole/internal/testxiaomi"
)

func TestXiaomiAdminImportAndProjectInvocation(t *testing.T) {
	h := start(t, t.TempDir(), true)
	cloud := testxiaomi.New()
	protocol := xiaomi.NewClient()
	protocol.HTTP = cloud.HTTPClient()
	m := xiaomi.New(h.store, h.core, protocol)
	access := router.New(h.store, router.Backend{Route: "builtin-access-1", Provisioner: h.manager, Invoker: h.manager}, router.Backend{Route: xiaomi.Route, Provisioner: m, Invoker: m})
	h.core.Provisioner, h.core.Invoker = access, access
	h.server.Close()
	h.server = httptest.NewServer(httpapi.New(h.core, h.auth, "http://localhost:8080", fstest.MapFS{"index.html": {Data: []byte("test")}}, httpapi.WithXiaomi(m)))
	t.Cleanup(func() { m.Close(); cloud.Close() })
	admin := h.login("admin", "test-password-only")
	noCSRF := &client{h: h, cookie: admin.cookie}
	status, _, _ := noCSRF.request("POST", "/admin/xiaomi/logins", map[string]any{"display_name": "实验室", "region": "cn"})
	if status != 403 {
		t.Fatal("扫码登录未检查CSRF", status)
	}
	anonymous := &client{h: h}
	if status, _, _ := anonymous.request("GET", "/admin/xiaomi/accounts", nil); status != 401 {
		t.Fatal("匿名用户可读取账号")
	}
	admin.do("POST", "/admin/xiaomi/logins", map[string]any{"display_name": "实验室", "region": "sg"}, 400)
	login := admin.do("POST", "/admin/xiaomi/logins", map[string]any{"display_name": "实验室", "region": "cn"}, 201)
	loginID := login["id"].(string)
	status, image, headers := admin.request("GET", "/admin/xiaomi/logins/"+loginID+"/qr", nil)
	if status != 200 || headers.Get("Content-Type") != "image/png" || headers.Get("Cache-Control") != "no-store" || len(image) == 0 {
		t.Fatal("二维码代理或缓存控制错误", status)
	}
	admin.do("POST", "/admin/users", map[string]any{"username": "other-admin", "password": "other-admin-password", "role": "admin"}, 201)
	other := h.login("other-admin", "other-admin-password")
	other.do("GET", "/admin/xiaomi/logins/"+loginID, nil, 404)
	cloud.Confirm(testxiaomi.UserID)
	var accountID string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		v := admin.do("GET", "/admin/xiaomi/logins/"+loginID, nil, 200)
		if v["status"] == "succeeded" {
			accountID = v["account_id"].(string)
			break
		}
		if v["status"] != "pending" {
			t.Fatal("扫码失败", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if accountID == "" {
		t.Fatal("扫码未完成")
	}
	base := "/admin/xiaomi/accounts/" + accountID
	discovery := admin.do("POST", base+"/discovery", map[string]any{}, 200)
	discoveryID := discovery["id"].(string)
	admin.do("POST", base+"/preview", map[string]any{"discovery_id": discoveryID, "did": "switch-1"}, 200)
	admin.do("POST", base+"/preview", map[string]any{"discovery_id": discoveryID, "did": "sensor-1"}, 400)
	imports := admin.do("POST", base+"/imports", map[string]any{"discovery_id": discoveryID, "dids": []string{"switch-1", "not-in-account"}}, 200)
	items := imports["items"].([]any)
	device := items[0].(map[string]any)["device"].(map[string]any)
	deviceID := device["id"].(string)
	if items[1].(map[string]any)["error"] == nil {
		t.Fatal("伪造DID可批量导入")
	}
	for _, path := range []string{"/admin/xiaomi/accounts", "/admin/devices/" + deviceID} {
		status, body, _ := admin.request("GET", path, nil)
		if status != 200 {
			t.Fatal(status)
		}
		for _, secret := range []string{"fixture-service", "fixture-pass", "fixture-cuser", "ssecurity", "payload", "must-not-leak-device-token"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("管理响应泄漏账号凭据", path)
			}
		}
	}
	noCSRF.do("POST", "/admin/devices/"+deviceID+"/refresh-capabilities", map[string]any{}, 403)
	anonymous.do("POST", "/admin/devices/"+deviceID+"/refresh-capabilities", map[string]any{}, 401)
	refreshed := admin.do("POST", "/admin/devices/"+deviceID+"/refresh-capabilities", map[string]any{}, 200)
	if refreshed["id"] != deviceID || refreshed["revision"].(float64) != device["revision"].(float64)+1 {
		t.Fatal("refresh replaced identity or lost revision", refreshed)
	}
	project := admin.project("xiaomi-switch")
	pid := project["id"].(string)
	admin.binding(pid, deviceID, "workbench_switch", "switch.2.1.set")
	token := admin.tokenFor(pid, []string{"docs:read", "services:invoke"})
	agent := &client{h: h, token: token}
	agent.do("GET", "/admin/xiaomi/accounts", nil, 403)
	agent.do("POST", "/admin/devices/"+deviceID+"/refresh-capabilities", map[string]any{}, 403)
	agent.do("POST", base+"/discovery", map[string]any{}, 403)
	result := agent.do("POST", "/projects/xiaomi-switch/services/workbench_switch/invoke", map[string]any{"parameters": map[string]any{"enabled": true}}, 200)
	if result["confirmation"] != "acknowledged" || result["source"] != "cloud" || result["observed_at"] != nil {
		t.Fatal("项目API未保留云端确认语义", result)
	}
	admin.do("DELETE", base, nil, 409)
	admin.do("DELETE", "/admin/devices/"+deviceID, nil, 409)
	// 开始审计写入失败必须阻止米家控制，不能只在串口路径保证这一点。
	if _, err := h.store.DB.Exec("CREATE TRIGGER fail_xiaomi_audit BEFORE INSERT ON invocations BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	agent.do("POST", "/projects/xiaomi-switch/services/workbench_switch/invoke", map[string]any{"parameters": map[string]any{"enabled": false}}, 500)
	writes, _, _, protocolError := cloud.Stats()
	if writes != 1 || protocolError != "" {
		t.Fatal("审计失败仍发送控制或模拟协议不匹配", writes, protocolError)
	}
	if _, err := h.store.DB.Exec("DROP TRIGGER fail_xiaomi_audit"); err != nil {
		t.Fatal(err)
	}
	_, raw, _ := admin.request("GET", "/projects/xiaomi-switch/docs", nil)
	var doc string
	json.Unmarshal(raw, &doc)
	if !strings.Contains(string(raw), "acknowledged") {
		t.Fatal("项目文档未说明接收确认")
	}
	admin.do("POST", "/admin/users", map[string]any{"username": "xiaomi-member", "password": "member-password", "role": "member"}, 201)
	member := h.login("xiaomi-member", "member-password")
	member.do("GET", "/admin/xiaomi/accounts", nil, 403)
	member.do("POST", base+"/imports", map[string]any{"discovery_id": discoveryID, "dids": []string{"switch-2"}}, 403)
}
