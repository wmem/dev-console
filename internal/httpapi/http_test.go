package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/access/transport"
	"devconsole/internal/auth"
	"devconsole/internal/core"
	"devconsole/internal/httpapi"
	"devconsole/internal/storage"
	"devconsole/internal/testdevice"
)

type harness struct {
	t       *testing.T
	dir     string
	store   *storage.Store
	core    *core.Core
	manager *manager.Manager
	auth    *auth.Auth
	server  *httptest.Server
	states  map[string]*testdevice.State
	opts    map[string]testdevice.Options
	mu      sync.Mutex
}
type client struct {
	h      *harness
	cookie string
	csrf   string
	token  string
}

func start(t *testing.T, dir string, init bool) *harness {
	t.Helper()
	s, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	k := core.New(s)
	if e = k.Recover(); e != nil {
		t.Fatal(e)
	}
	m := manager.New(s, k)
	h := &harness{t: t, dir: dir, store: s, core: k, manager: m, auth: auth.New(s), states: map[string]*testdevice.State{}, opts: map[string]testdevice.Options{}}
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		a, b := net.Pipe()
		h.mu.Lock()
		state := h.states[cfg.Path]
		if state == nil {
			state = testdevice.NewState()
			h.states[cfg.Path] = state
		}
		opt := h.opts[cfg.Path]
		if cfg.Address > 0 {
			opt.Protocol = "modbus"
		}
		h.mu.Unlock()
		go testdevice.Serve(b, state, opt)
		return &testdevice.Port{Conn: a}, nil
	}
	k.Provisioner = m
	k.Invoker = m
	if init {
		if _, e = h.auth.Bootstrap("admin", "test-password-only"); e != nil {
			t.Fatal(e)
		}
	}
	if e = m.Restore(context.Background()); e != nil {
		t.Fatal(e)
	}
	h.server = httptest.NewServer(httpapi.New(k, h.auth, "http://192.168.1.20:8080", fstest.MapFS{"index.html": {Data: []byte("<html>测试页面</html>")}, "assets/main.js": {Data: []byte("test")}}))
	t.Cleanup(h.close)
	return h
}
func (h *harness) close() {
	if h.server != nil {
		h.server.Close()
		h.server = nil
		h.manager.Close()
		h.store.Close()
	}
}
func (h *harness) login(name, password string) *client {
	h.t.Helper()
	x := &client{h: h}
	status, b, headers := x.request("POST", "/auth/login", map[string]any{"username": name, "password": password})
	if status != 200 {
		h.t.Fatalf("登录失败 %d %s", status, b)
	}
	var out struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(b, &out)
	x.csrf = out.CSRF
	for _, header := range headers.Values("Set-Cookie") {
		if strings.HasPrefix(header, "dc_session=") {
			x.cookie = strings.Split(header, ";")[0]
		}
	}
	return x
}
func (x *client) request(method, path string, v any) (int, []byte, http.Header) {
	x.h.t.Helper()
	var b []byte
	if v != nil {
		b, _ = json.Marshal(v)
	}
	req, _ := http.NewRequest(method, x.h.server.URL+"/api/v1"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if x.cookie != "" {
		req.Header.Set("Cookie", x.cookie)
	}
	if x.csrf != "" {
		req.Header.Set("X-CSRF-Token", x.csrf)
	}
	if x.token != "" {
		req.Header.Set("Authorization", "Bearer "+x.token)
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		x.h.t.Fatal(e)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data, res.Header
}
func (x *client) do(method, path string, v any, want int) map[string]any {
	x.h.t.Helper()
	status, b, _ := x.request(method, path, v)
	if status != want {
		x.h.t.Fatalf("%s %s: %d != %d: %s", method, path, status, want, b)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
func (x *client) device(path string) string {
	x.h.t.Helper()
	d := x.do("POST", "/admin/devices", map[string]any{"template_key": "itech-it6722", "display_name": "模拟电源", "connection_config": map[string]any{"path": path}}, 201)
	return d["id"].(string)
}
func (x *client) project(key string) map[string]any {
	return x.do("POST", "/admin/projects", map[string]any{"key": key, "display_name": key, "enabled": true, "purpose": "包含引号\"、反引号`、HTML<script>与\n换行"}, 201)
}
func (x *client) binding(project, device, key, service string) map[string]any {
	return x.do("POST", "/admin/projects/"+project+"/services", map[string]any{"key": key, "display_name": key, "enabled": true, "device_id": device, "device_service_key": service}, 201)
}
func (x *client) tokenFor(project string, scopes []string) string {
	return x.do("POST", "/admin/projects/"+project+"/tokens", map[string]any{"name": "测试调用方", "scopes": scopes}, 201)["secret"].(string)
}
func TestProjectFlowAndAuthorization(t *testing.T) {
	h := start(t, t.TempDir(), true)
	admin := h.login("admin", "test-password-only")
	device := admin.device("/tmp/scpi-a")
	pa := admin.project("project-a")
	pb := admin.project("project-b")
	ba := admin.binding(pa["id"].(string), device, "power", "output.set")
	admin.binding(pb["id"].(string), device, "power", "output.set")
	ta := &client{h: h, token: admin.tokenFor(pa["id"].(string), nil)}
	tb := &client{h: h, token: admin.tokenFor(pb["id"].(string), nil)}
	for _, x := range []struct {
		client  *client
		key     string
		enabled bool
	}{{ta, "project-a", true}, {tb, "project-b", false}} {
		r := x.client.do("POST", "/projects/"+x.key+"/services/power/invoke", map[string]any{"parameters": map[string]any{"enabled": x.enabled}}, 200)
		if r["confirmation"] != "verified" || r["source"] != "live" {
			t.Fatal(r)
		}
	}
	ta.do("GET", "/projects/project-b", nil, 403)
	ta.do("GET", "/admin/devices", nil, 403)
	ta.do("GET", "/projects", nil, 403)
	ta.do("GET", "/projects/project-a/invocations", nil, 403)
	ta.do("POST", "/projects/project-a/services/power/invoke", map[string]any{"parameters": map[string]any{"enabled": true, "unknown": 1}}, 400)
	mixed := &client{h: h, token: "bad", cookie: admin.cookie, csrf: admin.csrf}
	mixed.do("GET", "/admin/devices", nil, 401)
	noCSRF := &client{h: h, cookie: admin.cookie}
	noCSRF.do("POST", "/admin/projects", map[string]any{}, 403)
	ba["device_service_key"] = "measurement.read"
	admin.do("PATCH", "/admin/projects/"+pa["id"].(string)+"/services/"+ba["id"].(string), ba, 409)
	status, doc, _ := ta.request("GET", "/projects/project-a/docs", nil)
	if status != 200 || strings.Contains(string(doc), ta.token) || strings.Contains(string(doc), "<script>") {
		t.Fatal("文档访问、转义或脱敏失败", status)
	}
	spec := ta.do("GET", "/projects/project-a/openapi.json", nil, 200)
	if spec["openapi"] != "3.1.0" {
		t.Fatal(spec)
	}
	admin.do("DELETE", "/admin/devices/"+device, nil, 409)
	u := admin.do("POST", "/admin/users", map[string]any{"username": "member", "password": "member-password", "role": "member"}, 201)
	if _, ok := u["password_hash"]; ok {
		t.Fatal("密码哈希泄漏")
	}
	member := h.login("member", "member-password")
	member.do("GET", "/admin/users", nil, 403)
	member.do("GET", "/projects/project-a", nil, 403)
	admin.do("PUT", "/admin/projects/"+pa["id"].(string)+"/members", map[string]any{"member_ids": []string{u["id"].(string)}, "revision": pa["revision"]}, 200)
	member.do("GET", "/projects/project-a", nil, 200)
	member.do("GET", "/projects/project-b", nil, 403)
	member.do("GET", "/projects/project-a/invocations", nil, 200)
	readonly := &client{h: h, token: admin.tokenFor(pa["id"].(string), []string{"docs:read"})}
	readonly.do("GET", "/projects/project-a/docs", nil, 200)
	readonly.do("POST", "/projects/project-a/services/power/invoke", map[string]any{"parameters": map[string]any{"enabled": true}}, 403)
	ts, _ := h.auth.Tokens(pa["id"].(string))
	for _, tk := range ts {
		if tk.Prefix == ta.token[:11] {
			admin.do("DELETE", "/admin/projects/"+pa["id"].(string)+"/tokens/"+tk.ID, nil, 200)
		}
	}
	ta.do("GET", "/projects/project-a/docs", nil, 401)
	admin.do("POST", "/admin/users/"+u["id"].(string)+"/reset-password", map[string]any{"password": "new-member-password"}, 200)
	member.do("GET", "/auth/me", nil, 401)
	us, _ := h.auth.Users()
	for _, u := range us {
		if u.Role == "admin" {
			admin.do("PATCH", "/admin/users/"+u.ID, map[string]any{"enabled": false, "role": "admin"}, 409)
		}
	}
	tokens, _ := storage.List[auth.Token](h.store, "access_tokens")
	for _, tk := range tokens {
		if tk.Digest == ta.token || tk.Digest == "" {
			t.Fatal("Token未使用摘要保存")
		}
	}
}
func TestBusyAcrossServicesAndIndependentPorts(t *testing.T) {
	h := start(t, t.TempDir(), true)
	x := h.login("admin", "test-password-only")
	a := x.device("/tmp/busy-a")
	b := x.device("/tmp/busy-b")
	project := x.project("busy-project")
	pid := project["id"].(string)
	x.binding(pid, a, "read", "measurement.read")
	x.binding(pid, a, "power", "output.set")
	x.binding(pid, b, "independent", "measurement.read")
	h.manager.Close()
	h.manager = manager.New(h.store, h.core)
	h.core.Invoker = h.manager
	h.core.Provisioner = h.manager
	seen := make(chan string, 10)
	gate := make(chan struct{})
	h.manager.Open = func(cfg transport.Config) (transport.Port, error) {
		u, v := net.Pipe()
		opt := testdevice.Options{}
		if cfg.Path == "/tmp/busy-a" {
			opt.Seen = seen
			opt.Gate = gate
		}
		go testdevice.Serve(v, testdevice.NewState(), opt)
		return &testdevice.Port{Conn: u}, nil
	}
	if e := h.manager.Restore(context.Background()); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() {
		status, _, _ := x.request("POST", "/projects/busy-project/services/read/invoke", map[string]any{"parameters": map[string]any{}})
		done <- status
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("设备调用未开始")
	}
	start := time.Now()
	x.do("POST", "/projects/busy-project/services/power/invoke", map[string]any{"parameters": map[string]any{"enabled": true}}, 409)
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("busy发生等待")
	}
	x.do("POST", "/projects/busy-project/services/independent/invoke", map[string]any{"parameters": map[string]any{}}, 200)
	close(gate)
	if status := <-done; status != 200 {
		t.Fatal(status)
	}
}
func TestRestartAndDeletion(t *testing.T) {
	dir := t.TempDir()
	h := start(t, dir, true)
	x := h.login("admin", "test-password-only")
	d := x.device("/tmp/restart")
	p := x.project("persistent")
	binding := x.binding(p["id"].(string), d, "voltage", "voltage.set")
	token := x.tokenFor(p["id"].(string), nil)
	h.close()
	h2 := start(t, dir, false)
	agent := &client{h: h2, token: token}
	agent.do("POST", "/projects/persistent/services/voltage/invoke", map[string]any{"parameters": map[string]any{"voltage": .29}}, 200)
	all, e := h2.core.Devices()
	if e != nil || len(all) != 1 || all[0].ID != d {
		t.Fatal(all, e)
	}
	admin := h2.login("admin", "test-password-only")
	admin.do("DELETE", "/admin/projects/"+p["id"].(string)+"/services/"+binding["id"].(string), nil, 200)
	admin.do("DELETE", "/admin/devices/"+d, nil, 200)
	h2.close()
	h3 := start(t, dir, false)
	all, e = h3.core.Devices()
	if e != nil || len(all) != 0 {
		t.Fatal("删除设备重启复活", all, e)
	}
	configs, e := storage.List[manager.Saved](h3.store, "access_configs")
	if e != nil || len(configs) != 0 {
		t.Fatal(configs, e)
	}
}
func TestBindingDefaultsRevisionsAndOffline(t *testing.T) {
	h := start(t, t.TempDir(), true)
	x := h.login("admin", "test-password-only")
	d := x.device("/tmp/binding-a")
	other := x.device("/tmp/binding-b")
	p := x.project("contracts")
	pid := p["id"].(string)
	b := x.binding(pid, d, "voltage", "voltage.set")
	b["default_parameters"] = map[string]any{"voltage": .29}
	b = x.do("PATCH", "/admin/projects/"+pid+"/services/"+b["id"].(string), b, 200)
	old := map[string]any{}
	for k, v := range b {
		old[k] = v
	}
	b["device_id"] = other
	b = x.do("PATCH", "/admin/projects/"+pid+"/services/"+b["id"].(string), b, 200)
	x.do("PATCH", "/admin/projects/"+pid+"/services/"+b["id"].(string), old, 409)
	r := x.do("POST", "/projects/contracts/services/voltage/invoke", map[string]any{"parameters": map[string]any{}}, 200)
	if r["device_id"] != other {
		t.Fatal("兼容改绑未生效")
	}
	detail := x.do("GET", "/admin/devices/"+other, nil, 200)
	device := detail["device"].(map[string]any)
	cfg := detail["connection_config"].(map[string]any)
	cfg["max_voltage"] = 10.0
	x.do("PATCH", "/admin/devices/"+other, map[string]any{"display_name": device["display_name"], "revision": device["revision"], "connection_config": cfg}, 200)
	x.do("POST", "/projects/contracts/services/voltage/invoke", map[string]any{"parameters": map[string]any{}}, 409)
	empty := x.binding(pid, "", "unbound", "")
	_ = empty
	x.do("POST", "/projects/contracts/services/unbound/invoke", map[string]any{"parameters": map[string]any{}}, 409)
	x.do("POST", "/admin/devices", map[string]any{"template_key": "itech-it6722", "display_name": "离线设备", "connection_config": map[string]any{"path": "/tmp/offline"}}, 201)
	anon := &client{h: h}
	anon.do("GET", "/projects/contracts/docs", nil, 401)
}
func TestHTTPStaticAndErrorBoundaries(t *testing.T) {
	h := start(t, t.TempDir(), true)
	for _, tc := range []struct {
		path    string
		status  int
		content string
	}{{"/projects/deep-link", 200, "text/html"}, {"/assets/main.js", 200, "javascript"}, {"/assets/missing.js", 404, "text/plain"}, {"/api/v1/not-found", 404, "application/json"}, {"/healthz", 200, "application/json"}} {
		res, e := http.Get(h.server.URL + tc.path)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != tc.status || !strings.Contains(res.Header.Get("Content-Type"), tc.content) {
			t.Fatal(tc, res.StatusCode, res.Header)
		}
	}
}

func TestAuditFailurePreventsPhysicalWrite(t *testing.T) {
	h := start(t, t.TempDir(), true)
	x := h.login("admin", "test-password-only")
	d := x.device("/tmp/audit-write")
	p := x.project("audit")
	x.binding(p["id"].(string), d, "power", "output.set")
	if _, e := h.store.DB.Exec("CREATE TRIGGER reject_invocation BEFORE INSERT ON invocations BEGIN SELECT RAISE(ABORT,'模拟审计写入失败'); END"); e != nil {
		t.Fatal(e)
	}
	x.do("POST", "/projects/audit/services/power/invoke", map[string]any{"parameters": map[string]any{"enabled": true}}, 500)
	h.mu.Lock()
	state := h.states["/tmp/audit-write"]
	h.mu.Unlock()
	_, _, _, writes := state.Snapshot()
	if writes != 0 {
		t.Fatal("审计开始记录失败后仍发送了控制命令")
	}
}

func TestDocsDefaultsMatchRuntimeAndDeviceRangeRevision(t *testing.T) {
	h := start(t, t.TempDir(), true)
	x := h.login("admin", "test-password-only")
	d := x.device("/tmp/doc-contract")
	p := x.project("doc-contract")
	pid := p["id"].(string)
	b := x.binding(pid, d, "voltage", "voltage.set")
	b["default_parameters"] = map[string]any{"voltage": 5.0}
	x.do("PATCH", "/admin/projects/"+pid+"/services/"+b["id"].(string), b, 200)
	spec := x.do("GET", "/projects/doc-contract/openapi.json", nil, 200)
	raw, _ := json.Marshal(spec)
	if strings.Contains(string(raw), `"required":["voltage"]`) {
		t.Fatal("已有默认值的参数仍被文档标记为必填")
	}
	x.do("POST", "/projects/doc-contract/services/voltage/invoke", map[string]any{"parameters": map[string]any{}}, 200)
	x.do("POST", "/projects/doc-contract/services/voltage/invoke", map[string]any{}, 400)
	before, e := h.core.ProjectByID(pid)
	if e != nil {
		t.Fatal(e)
	}
	detail := x.do("GET", "/admin/devices/"+d, nil, 200)
	device := detail["device"].(map[string]any)
	cfg := detail["connection_config"].(map[string]any)
	cfg["max_voltage"] = 10.0
	x.do("PATCH", "/admin/devices/"+d, map[string]any{"display_name": device["display_name"], "revision": device["revision"], "connection_config": cfg}, 200)
	after, e := h.core.ProjectByID(pid)
	if e != nil || after.DocRevision <= before.DocRevision {
		t.Fatal("设备约束变更未更新项目文档版本", before.DocRevision, after.DocRevision, e)
	}
}

func TestReassignPortKeepsDeviceBindingsAndToken(t *testing.T) {
	dir := t.TempDir()
	h := start(t, dir, true)
	x := h.login("admin", "test-password-only")
	id := x.device("/tmp/reassign-old")
	p := x.project("reassign-port")
	b := x.binding(p["id"].(string), id, "settings", "settings.read")
	token := x.tokenFor(p["id"].(string), nil)
	h.mu.Lock()
	h.states["/tmp/reassign-new"] = &testdevice.State{V: 9, I: .5}
	h.mu.Unlock()
	detail := x.do("GET", "/admin/devices/"+id, nil, 200)
	device := detail["device"].(map[string]any)
	cfg := detail["connection_config"].(map[string]any)
	cfg["path"] = "/tmp/reassign-new"
	updated := x.do("PATCH", "/admin/devices/"+id, map[string]any{"display_name": device["display_name"], "revision": device["revision"], "connection_config": cfg}, 200)
	if updated["id"] != id {
		t.Fatal("重新选择串口改变了设备ID")
	}
	agent := &client{h: h, token: token}
	r := agent.do("POST", "/projects/reassign-port/services/settings/invoke", map[string]any{"parameters": map[string]any{}}, 200)
	if r["result"].(map[string]any)["voltage"] != 9.0 || r["device_id"] != id {
		t.Fatal("旧URL或Token未使用新串口", r)
	}
	bindings, e := h.core.Bindings(p["id"].(string), false)
	if e != nil || len(bindings) != 1 || bindings[0].ID != b["id"] || bindings[0].DeviceID != id {
		t.Fatal(bindings, e)
	}
	h.close()
	fresh := start(t, dir, false)
	sameAgent := &client{h: fresh, token: token}
	sameAgent.do("POST", "/projects/reassign-port/services/settings/invoke", map[string]any{"parameters": map[string]any{}}, 200)
	var saved manager.Saved
	if e = fresh.store.Get("access_configs", id, &saved); e != nil || saved.Config.Path != "/tmp/reassign-new" {
		t.Fatal("重启丢失新串口配置", saved, e)
	}
}

func TestConnectionDiscoveryIsAdminOnly(t *testing.T) {
	h := start(t, t.TempDir(), true)
	x := h.login("admin", "test-password-only")
	h.manager.Ports = func() ([]transport.PortInfo, error) {
		return []transport.PortInfo{{Path: "/dev/ttyACM9", ByPath: []string{"/dev/serial/by-path/fixed"}}}, nil
	}
	status, data, _ := x.request("GET", "/admin/access-templates/itech-it6722/connections", nil)
	if status != 200 || !strings.Contains(string(data), "ttyACM9") || !strings.Contains(string(data), "usb_port") {
		t.Fatal(status, string(data))
	}
	p := x.project("discovery-permission")
	token := &client{h: h, token: x.tokenFor(p["id"].(string), nil)}
	token.do("GET", "/admin/access-templates/itech-it6722/connections", nil, 403)
	x.do("GET", "/admin/access-templates/unknown/connections", nil, 400)
}
