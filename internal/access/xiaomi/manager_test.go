package xiaomi_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/access/router"
	"devconsole/internal/access/xiaomi"
	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"devconsole/internal/core"
	"devconsole/internal/secretbox"
	"devconsole/internal/storage"
	"devconsole/internal/testxiaomi"
)

type fixture struct {
	cloud   *testxiaomi.Cloud
	store   *storage.Store
	core    *core.Core
	manager *xiaomi.Manager
	client  *xiaomi.Client
}

func TestCancellationAndResultAuditFailure(t *testing.T) {
	f := setup(t)
	a := f.account(t)
	ds := f.discover(t, a)
	d := f.device(t, a, ds.ID, "switch-1")
	entered, release := f.cloud.Block()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := f.invoke(ctx, d.ID, "switch.2.1.set", map[string]any{"enabled": true})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("控制未到达模拟云")
	}
	cancel()
	select {
	case err := <-finished:
		if c.AsError(err).Outcome != "unknown" {
			t.Fatal("取消被解释为动作未发生", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消未有界结束")
	}
	release()
	writes, _, _, _ := f.cloud.Stats()
	if writes > 1 {
		t.Fatal("取消后控制被重发")
	}
	// 独立设备验证结束审计失败；不等待或绕过上一设备的隔离。
	other := f.device(t, a, ds.ID, "switch-2")
	if _, err := f.store.DB.Exec("CREATE TRIGGER fail_result BEFORE UPDATE ON invocations WHEN json_extract(NEW.data,'$.status')='succeeded' BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	result, err := f.invoke(context.Background(), other.ID, "switch.2.1.set", map[string]any{"enabled": true})
	if err == nil || result.Status != "failed" || result.Error == nil || result.Error.Outcome != "unknown" {
		t.Fatal("结束审计失败仍被响应为执行成功", result, err)
	}
	after, _, _, _ := f.cloud.Stats()
	if after != writes+1 {
		t.Fatal("结果保存失败后控制被重发", after)
	}
}

func setup(t *testing.T) *fixture {
	t.Helper()
	cloud := testxiaomi.New()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := xiaomi.NewClient()
	client.HTTP = cloud.HTTPClient()
	k := core.New(store)
	m := xiaomi.New(store, k, client)
	serial := manager.New(store, k)
	r := router.New(store, router.Backend{Route: "builtin-access-1", Provisioner: serial, Invoker: serial}, router.Backend{Route: xiaomi.Route, Provisioner: m, Invoker: m})
	k.Invoker, k.Provisioner = r, r
	f := &fixture{cloud: cloud, store: store, core: k, manager: m, client: client}
	t.Cleanup(func() { m.Close(); serial.Close(); cloud.Close(); store.Close() })
	return f
}

func waitLogin(t *testing.T, m *xiaomi.Manager, id string, want string) xiaomi.LoginStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.Login("owner", id)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status != "pending" {
			if status.Status != want {
				t.Fatalf("登录状态 %s，预期 %s：%s", status.Status, want, status.Error)
			}
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("模拟扫码未完成")
	return xiaomi.LoginStatus{}
}

func (f *fixture) account(t *testing.T) string {
	t.Helper()
	login, err := f.manager.StartLogin(context.Background(), "owner", "实验室", "cn", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.Login("other-admin", login.ID); c.AsError(err).Code != "NOT_FOUND" {
		t.Fatal("扫码会话未隔离管理员")
	}
	if _, _, err = f.manager.QRImage("other-admin", login.ID); c.AsError(err).Code != "NOT_FOUND" {
		t.Fatal("二维码未隔离管理员")
	}
	if image, typ, err := f.manager.QRImage("owner", login.ID); err != nil || len(image) == 0 || typ != "image/png" {
		t.Fatal("二维码不可用", err)
	}
	f.cloud.Confirm(testxiaomi.UserID)
	return waitLogin(t, f.manager, login.ID, "succeeded").AccountID
}

func (f *fixture) discover(t *testing.T, account string) xiaomi.DiscoveryResult {
	t.Helper()
	d, err := f.manager.Discover(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Devices) != 3 {
		t.Fatal("分页或共享设备去重错误", len(d.Devices))
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "must-not-leak") || strings.Contains(string(b), "fixture-service") {
		t.Fatal("发现响应泄漏凭据")
	}
	return d
}

func (f *fixture) device(t *testing.T, account, discovery, did string) c.Device {
	t.Helper()
	p, err := f.manager.Preview(context.Background(), account, discovery, did)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Services) != 4 {
		t.Fatal("没有按通道注册开关，或误把童锁当作开关", len(p.Services))
	}
	name, config, err := f.manager.PrepareImport(account, discovery, did)
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.core.CreateDevice(context.Background(), xiaomi.TemplateKey, name, config)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var principal = auth.Principal{Kind: "user", Role: "admin", ID: "test-admin"}

func (f *fixture) invoke(ctx context.Context, id, key string, p map[string]any) (core.Record, error) {
	return f.core.InvokeDevice(ctx, principal, id, key, p)
}

func TestAccountDiscoverySwitchesAndProjectContract(t *testing.T) {
	f := setup(t)
	a := f.account(t)
	discovery := f.discover(t, a)
	if _, err := f.manager.Preview(context.Background(), a, discovery.ID, "sensor-1"); c.AsError(err).Code != "INVALID_ARGUMENT" {
		t.Fatal("不支持的设备未拒绝", err)
	}
	if _, _, err := f.manager.PrepareImport(a, discovery.ID, "switch-1"); c.AsError(err).Code != "INVALID_ARGUMENT" {
		t.Fatal("跳过预览仍可导入")
	}
	d := f.device(t, a, discovery.ID, "switch-1")
	if _, _, err := f.manager.PrepareImport(a, discovery.ID, "not-in-account"); c.AsError(err).Code != "INVALID_ARGUMENT" {
		t.Fatal("伪造DID通过")
	}
	_, config, _ := f.manager.PrepareImport(a, discovery.ID, "switch-1")
	if _, err := f.core.CreateDevice(context.Background(), xiaomi.TemplateKey, "重复", config); c.AsError(err).Code != "CONFIG_CONFLICT" {
		t.Fatal("重复设备可绕过busy")
	}
	read, err := f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{})
	if err != nil || read.Source != "cache" || read.Confirmation != "read" || read.ObservedAt == nil || read.ObservedAt.Format(time.RFC3339) != "2026-01-02T03:04:05Z" {
		t.Fatal("云端时间或来源不正确", read, err)
	}
	result, err := f.invoke(context.Background(), d.ID, "switch.3.1.set", map[string]any{"enabled": true})
	if err != nil || result.Confirmation != "acknowledged" || result.Source != "cloud" || result.ObservedAt != nil {
		t.Fatal("云端接收被冒充为状态核验", result, err)
	}
	writes, reads, _, protocolError := f.cloud.Stats()
	if writes != 1 || reads != 1 || protocolError != "" {
		t.Fatal("请求重发或协议不匹配", writes, reads, protocolError)
	}
	f.cloud.SetMissingTime(true)
	read, err = f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{})
	if err != nil || read.ObservedAt != nil {
		t.Fatal("来源缺时间仍虚构采集时间", read, err)
	}
	if _, err = f.invoke(context.Background(), d.ID, "switch.2.1.set", map[string]any{"enabled": "true"}); c.AsError(err).Code != "INVALID_ARGUMENT" {
		t.Fatal("字符串布尔值被接受")
	}
	project, err := f.core.SaveProject(core.Project{Key: "switch-test", DisplayName: "开关项目", Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.core.SaveBinding(core.Binding{ProjectID: project.ID, Key: "light", DisplayName: "工作台开关", Enabled: true, DeviceID: d.ID, DeviceServiceKey: "switch.2.1.set"}, true)
	if err != nil {
		t.Fatal(err)
	}
	doc, spec, err := f.core.Document(project.Key, principal, "http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `"confirmation": "acknowledged"`) || !strings.Contains(doc, `"source": "cloud"`) {
		t.Fatal("生成文档夸大了确认等级或来源")
	}
	encoded, _ := json.Marshal(spec)
	if !strings.Contains(string(encoded), "requested_enabled") {
		t.Fatal("OpenAPI不来自设备契约")
	}
	if err = f.core.DeleteDevice(context.Background(), d.ID); c.AsError(err).Code != "RESOURCE_REFERENCED" {
		t.Fatal("项目引用未阻止删除")
	}
	if err = f.manager.DeleteAccount(a); c.AsError(err).Code != "RESOURCE_REFERENCED" {
		t.Fatal("仍有设备时可删除账号")
	}
	if err = f.core.DeleteBinding(project.ID, binding.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.core.DeleteDevice(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.DeleteAccount(a); err != nil {
		t.Fatal(err)
	}
	if len(f.manager.Accounts()) != 0 {
		t.Fatal("账号未删除")
	}
}

func TestBusyCoversAllChannelsAndAllowsOtherDevices(t *testing.T) {
	f := setup(t)
	a := f.account(t)
	ds := f.discover(t, a)
	d := f.device(t, a, ds.ID, "switch-1")
	other := f.device(t, a, ds.ID, "switch-2")
	entered, release := f.cloud.Block()
	finished := make(chan error, 2)
	go func() {
		_, err := f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("未进入模拟云端")
	}
	started := time.Now()
	if _, err := f.invoke(context.Background(), d.ID, "switch.3.1.set", map[string]any{"enabled": true}); c.AsError(err).Code != "RESOURCE_BUSY" || time.Since(started) > time.Second {
		t.Fatal("同设备不同通道排队或绕过busy", err)
	}
	config, _ := f.manager.Config(context.Background(), d.ID)
	if _, err := f.core.UpdateDevice(context.Background(), d.ID, "改名", config, d.Revision); c.AsError(err).Code != "RESOURCE_BUSY" {
		t.Fatal("忙碌时可以修改", err)
	}
	if err := f.core.DeleteDevice(context.Background(), d.ID); c.AsError(err).Code != "RESOURCE_BUSY" {
		t.Fatal("忙碌时可以删除", err)
	}
	go func() {
		_, err := f.invoke(context.Background(), other.ID, "switch.2.1.read", map[string]any{})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("独立设备被错误地串行化")
	}
	release()
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnknownOutcomeNeverRetriesOrReplaysOnRestore(t *testing.T) {
	f := setup(t)
	a := f.account(t)
	ds := f.discover(t, a)
	d := f.device(t, a, ds.ID, "switch-1")
	f.cloud.SetDropWrite(true)
	r, err := f.invoke(context.Background(), d.ID, "switch.2.1.set", map[string]any{"enabled": true})
	if c.AsError(err).Code != "OUTCOME_UNKNOWN" || r.Error.Outcome != "unknown" {
		t.Fatal("响应丢失未标未知", r, err)
	}
	writes, _, _, _ := f.cloud.Stats()
	if writes != 1 {
		t.Fatal("控制发生重试", writes)
	}
	if _, err = f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{}); c.AsError(err).Code != "RESOURCE_BUSY" {
		t.Fatal("未知结果未隔离")
	}
	f.manager.Close()
	restored := xiaomi.New(f.store, f.core, f.client)
	defer restored.Close()
	if err := f.core.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, requests, _ := f.cloud.Stats()
	time.Sleep(20 * time.Millisecond)
	newWrites, _, newRequests, _ := f.cloud.Stats()
	if newWrites != writes || newRequests != requests {
		t.Fatal("恢复时访问云端或重放输出")
	}
	current, _ := f.core.Device(d.ID)
	if current.ID != d.ID || current.Availability != "unknown" {
		t.Fatal("恢复时身份或状态错误")
	}
}

func TestReauthorizationIdentityAndTokenRefresh(t *testing.T) {
	f := setup(t)
	a := f.account(t)
	ds := f.discover(t, a)
	d := f.device(t, a, ds.ID, "switch-1")
	f.cloud.SetExpired(true)
	if _, err := f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{}); c.AsError(err).Code != "DEVICE_OFFLINE" {
		t.Fatal("登录过期未报告", err)
	}
	_, _, requests, _ := f.cloud.Stats()
	if _, err := f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{}); err != nil {
		t.Fatal("下一次显式调用不能先续期", err)
	}
	_, _, after, _ := f.cloud.Stats()
	if after != requests+1 {
		t.Fatal("自动重放业务请求")
	}
	login, err := f.manager.StartLogin(context.Background(), "owner", "实验室", "cn", a)
	if err != nil {
		t.Fatal(err)
	}
	f.cloud.Confirm("999999")
	waitLogin(t, f.manager, login.ID, "failed")
	if f.manager.Accounts()[0].UserID != testxiaomi.UserID {
		t.Fatal("重新扫码静默替换了账号身份")
	}
	if current, _ := f.core.Device(d.ID); current.ID != d.ID {
		t.Fatal("重登改变了设备身份")
	}
	login, err = f.manager.StartLogin(context.Background(), "owner", "实验室", "cn", a)
	if err != nil {
		t.Fatal(err)
	}
	f.cloud.Confirm(testxiaomi.UserID)
	waitLogin(t, f.manager, login.ID, "succeeded")
	if current, _ := f.core.Device(d.ID); current.ID != d.ID {
		t.Fatal("重登创建了重复设备")
	}
}

func TestCancelLoginAndBadPropertyReply(t *testing.T) {
	f := setup(t)
	login, err := f.manager.StartLogin(context.Background(), "owner", "实验室", "cn", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.manager.CancelLogin("owner", login.ID); err != nil {
		t.Fatal(err)
	}
	f.cloud.Confirm(testxiaomi.UserID)
	time.Sleep(10 * time.Millisecond)
	if len(f.manager.Accounts()) != 0 {
		t.Fatal("取消登录后仍保存账号")
	}
	a := f.account(t)
	ds := f.discover(t, a)
	d := f.device(t, a, ds.ID, "switch-1")
	f.cloud.SetWrongProperty(true)
	if _, err = f.invoke(context.Background(), d.ID, "switch.2.1.read", map[string]any{}); c.AsError(err).Code != "PROTOCOL_ERROR" {
		t.Fatal("其他设备的响应被接受", err)
	}
}

func TestAccountSecretsEncryptedAndKeyRequired(t *testing.T) {
	cloud := testxiaomi.New()
	defer cloud.Close()
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := xiaomi.NewClient()
	client.HTTP = cloud.HTTPClient()
	m := xiaomi.New(store, core.New(store), client)
	login, err := m.StartLogin(context.Background(), "owner", "密钥检查", "cn", "")
	if err != nil {
		t.Fatal(err)
	}
	cloud.Confirm(testxiaomi.UserID)
	waitLogin(t, m, login.ID, "succeeded")
	for _, table := range []string{"xiaomi_accounts", "xiaomi_credentials"} {
		var raw string
		if err := store.DB.QueryRow("SELECT data FROM " + table + " LIMIT 1").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"fixture-service", "fixture-pass", "fixture-cuser"} {
			if strings.Contains(raw, secret) {
				t.Fatal("账号凭据明文落库")
			}
		}
	}
	m.Close()
	store.Close()
	key := filepath.Join(dir, secretbox.KeyFile)
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if reopened, err := storage.Open(dir); err == nil {
		reopened.Close()
		t.Fatal("仅有小米账号密文时仍自动重建了密钥")
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatal("丢失的密钥被重新生成")
	}
}

func TestRefreshPreservesDeviceAndProjectBindingsWithoutPropertyRequests(t *testing.T) {
	f := setup(t)
	account := f.account(t)
	ds := f.discover(t, account)
	d := f.device(t, account, ds.ID, "switch-1")
	project, err := f.core.SaveProject(core.Project{Key: "refresh", DisplayName: "刷新验证", Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.core.SaveBinding(core.Binding{ProjectID: project.ID, Key: "outlet", DisplayName: "原通道", Enabled: true, DeviceID: d.ID, DeviceServiceKey: "switch.2.1.set"}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/oit.plug.cb7.spec.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "urn:miot-spec-v2:device:outlet:0000A002:oit-cb7:1:0000D030", testxiaomi.SpecURN))
	f.cloud.SetSpec(testxiaomi.SpecURN, raw)
	writes, reads, requests, _ := f.cloud.Stats()
	refreshed, err := f.core.RefreshDeviceCapabilities(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != d.ID || refreshed.DisplayName != d.DisplayName || refreshed.Revision != d.Revision+1 || len(refreshed.Services) != 14 {
		t.Fatal(refreshed)
	}
	var current core.Binding
	if err = f.store.Get("project_services", binding.ID, &current); err != nil || current.DeviceID != d.ID || current.DeviceServiceKey != "switch.2.1.set" {
		t.Fatal(current, err)
	}
	var persisted core.Project
	if err = f.store.Get("projects", project.ID, &persisted); err != nil || persisted.DocRevision <= project.DocRevision {
		t.Fatal("project docs revision", persisted, err)
	}
	w, r, q, problem := f.cloud.Stats()
	if w != writes || r != reads || q != requests || problem != "" {
		t.Fatal("refresh called account/property cloud", w, r, q, problem)
	}
	config, err := f.manager.Config(context.Background(), d.ID)
	if err != nil || strings.Contains(string(config), "discovery_id") {
		t.Fatal("ephemeral capability token persisted", err)
	}
	// A prepared refresh cannot overwrite a concurrent rename/revision.
	candidate, err := f.manager.PrepareCapabilityRefresh(context.Background(), d.ID, refreshed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := f.core.UpdateDevice(context.Background(), d.ID, "新名字", config, refreshed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.core.UpdateDevice(context.Background(), d.ID, d.DisplayName, candidate, refreshed.Revision); c.AsError(err).Code != "CONFIG_CONFLICT" {
		t.Fatal("stale refresh accepted", err)
	}
	restored, err := f.core.Device(d.ID)
	if err != nil || restored.DisplayName != renamed.DisplayName || len(restored.Services) != 14 {
		t.Fatal(restored, err)
	}
	if err = f.manager.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err = f.core.Device(d.ID)
	if err != nil || restored.ID != d.ID || len(restored.Services) != 14 {
		t.Fatal("refresh snapshot lost on restore", restored, err)
	}
}
