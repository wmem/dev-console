package xiaomi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"devconsole/internal/storage"
)

const Route = "xiaomi-cloud-1"
const TemplateKey = "xiaomi-miot-switch"

type deviceConfig struct {
	AccountID   string `json:"account_id"`
	DID         string `json:"did"`
	DiscoveryID string `json:"discovery_id,omitempty"`
}

type savedDevice struct {
	ID       string       `json:"id"`
	Config   deviceConfig `json:"connection_config"`
	Model    string       `json:"model"`
	SpecType string       `json:"spec_type"`
	Switches []Switch     `json:"switches"`
}

type target struct {
	saved      savedDevice
	busy       atomic.Bool
	quarantine time.Time // 在 mu 下读写；隔离结果未知后的后续请求。
}

type DiscoveryResult struct {
	ID        string         `json:"id"`
	ExpiresAt time.Time      `json:"expires_at"`
	Devices   []RemoteDevice `json:"devices"`
}

type Preview struct {
	Device   RemoteDevice `json:"device"`
	Services []c.Service  `json:"services"`
}

func (m *Manager) Discover(ctx context.Context, accountID string) (DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cred, release, err := m.credentials(ctx, accountID, false)
	if err != nil {
		return DiscoveryResult{}, err
	}
	defer release()
	devices, err := m.client.devices(ctx, cred)
	if err != nil {
		return DiscoveryResult{}, m.accountError(accountID, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	d := &discovery{id: auth.ID(), accountID: accountID, expires: time.Now().UTC().Add(10 * time.Minute), devices: map[string]RemoteDevice{}, prepared: map[string][]Switch{}}
	for i := range devices {
		for _, t := range m.targets {
			if t.saved.Config.DID == string(devices[i].DID) {
				devices[i].Imported = t.saved.ID
			}
		}
		d.devices[string(devices[i].DID)] = devices[i]
	}
	for id, old := range m.discovery {
		if old.accountID == accountID {
			delete(m.discovery, id)
		}
	}
	m.discovery[d.id] = d
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return DiscoveryResult{ID: d.id, ExpiresAt: d.expires, Devices: devices}, nil
}

func (m *Manager) candidateLocked(discoveryID, did string) (*discovery, RemoteDevice, error) {
	d := m.discovery[discoveryID]
	if d == nil || time.Now().After(d.expires) {
		return nil, RemoteDevice{}, c.Fail("CONFIG_CONFLICT", "设备发现清单已过期，请重新刷新")
	}
	v, ok := d.devices[did]
	if !ok {
		return nil, v, c.Fail("INVALID_ARGUMENT", "设备不在本次账号发现清单内")
	}
	return d, v, nil
}

func (m *Manager) Preview(ctx context.Context, accountID, discoveryID, did string) (Preview, error) {
	m.mu.Lock()
	d, device, err := m.candidateLocked(discoveryID, did)
	if err == nil && d.accountID != accountID {
		err = c.Fail("INVALID_ARGUMENT", "设备发现清单不属于此账号")
	}
	if err != nil {
		m.mu.Unlock()
		return Preview{}, err
	}
	if prepared := d.prepared[did]; len(prepared) > 0 {
		m.mu.Unlock()
		return Preview{Device: device, Services: switchServices(prepared)}, nil
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switches, err := m.client.switches(ctx, device.SpecType)
	if err != nil {
		return Preview{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, _, err := m.candidateLocked(discoveryID, did)
	if err != nil || current != d {
		return Preview{}, c.Fail("CONFIG_CONFLICT", "设备清单已更新，请重新选择设备")
	}
	d.prepared[did] = switches
	return Preview{Device: device, Services: switchServices(switches)}, nil
}

// PrepareImport 不发网络请求；必须先预览能力，才能把选中的候选交给核心登记。
func (m *Manager) PrepareImport(accountID, discoveryID, did string) (string, json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, device, err := m.candidateLocked(discoveryID, did)
	if err != nil {
		return "", nil, err
	}
	if d.accountID != accountID || len(d.prepared[did]) == 0 {
		return "", nil, c.Fail("INVALID_ARGUMENT", "请先预览该账号设备的开关能力")
	}
	name := strings.TrimSpace(device.Name)
	if name == "" {
		name = device.Model
	}
	if name == "" {
		name = "米家开关"
	}
	config, _ := json.Marshal(deviceConfig{AccountID: accountID, DID: did, DiscoveryID: discoveryID})
	return name, config, nil
}

func (m *Manager) ListTemplates(context.Context) []c.Template {
	return []c.Template{{Key: TemplateKey, Title: "米家云开关", CreationMode: "discovery", CanRefreshCapabilities: true, ConfigSchema: c.Object(map[string]c.Schema{"account_id": {Type: "string", Description: "接入账号"}, "did": {Type: "string", Description: "米家设备标识"}}, "account_id", "did")}}
}

func (m *Manager) DiscoverConnections(context.Context, string) ([]c.ConnectionChoice, error) {
	return []c.ConnectionChoice{}, nil
}

func parseConfig(raw json.RawMessage) (deviceConfig, error) {
	var cfg deviceConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil || cfg.AccountID == "" || cfg.DID == "" {
		return cfg, c.Fail("INVALID_ARGUMENT", "米家接入配置无效")
	}
	return cfg, nil
}

func device(saved savedDevice, name string) c.Device {
	return c.Device{ID: saved.ID, TemplateKey: TemplateKey, DisplayName: name, AccessRoute: Route, Availability: "unknown", LastError: "尚未读取米家云状态", Services: switchServices(saved.Switches), Revision: 1}
}

func (m *Manager) Create(ctx context.Context, key, name string, raw json.RawMessage) (c.Device, error) {
	if key != TemplateKey {
		return c.Device{}, c.Fail("INVALID_ARGUMENT", "未知米家接入模板")
	}
	cfg, err := parseConfig(raw)
	if err != nil {
		return c.Device{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accounts[cfg.AccountID] == nil {
		return c.Device{}, c.Fail("NOT_FOUND", "小米账号不存在")
	}
	d, remote, err := m.candidateLocked(cfg.DiscoveryID, cfg.DID)
	if err != nil {
		return c.Device{}, err
	}
	if d.refreshDeviceID != "" || d.accountID != cfg.AccountID || len(d.prepared[cfg.DID]) == 0 {
		return c.Device{}, c.Fail("INVALID_ARGUMENT", "设备尚未完成开关能力预览")
	}
	for _, t := range m.targets {
		if t.saved.Config.DID == cfg.DID {
			return c.Device{}, c.Fail("CONFIG_CONFLICT", "此米家设备已经导入，请使用原设备；共享账号不能重复登记同一设备")
		}
	}
	cfg.DiscoveryID = ""
	saved := savedDevice{ID: auth.ID(), Config: cfg, Model: remote.Model, SpecType: remote.SpecType, Switches: append([]Switch(nil), d.prepared[cfg.DID]...)}
	v := device(saved, name)
	if err := m.store.Batch(storage.Change{Table: "xiaomi_devices", ID: saved.ID, Value: saved}, storage.Change{Table: "devices", ID: saved.ID, Value: v}); err != nil {
		return c.Device{}, err
	}
	m.targets[saved.ID] = &target{saved: saved}
	return v, nil
}

func (m *Manager) Config(ctx context.Context, id string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.targets[id]
	if t == nil {
		return nil, c.Fail("DEVICE_NOT_FOUND", "米家设备不存在")
	}
	return json.Marshal(t.saved.Config)
}

// PrepareCapabilityRefresh downloads only the public specification, never device properties.
func (m *Manager) PrepareCapabilityRefresh(ctx context.Context, id string, revision int) (json.RawMessage, error) {
	m.mu.Lock()
	t := m.targets[id]
	if t == nil {
		m.mu.Unlock()
		return nil, c.Fail("DEVICE_NOT_FOUND", "米家设备不存在")
	}
	saved := t.saved
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switches, err := m.client.switches(ctx, saved.SpecType)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var current c.Device
	if m.targets[id] != t {
		return nil, c.Fail("CONFIG_CONFLICT", "设备已变化，请刷新")
	}
	if err := m.store.Get("devices", id, &current); err != nil {
		return nil, err
	}
	if current.Revision != revision {
		return nil, c.Fail("CONFIG_CONFLICT", "设备配置已变化，请刷新")
	}
	m.cleanupLocked()
	d := &discovery{id: auth.ID(), accountID: saved.Config.AccountID, refreshDeviceID: id, revision: revision, expires: time.Now().Add(2 * time.Minute), devices: map[string]RemoteDevice{saved.Config.DID: {SpecType: saved.SpecType, Model: saved.Model}}, prepared: map[string][]Switch{saved.Config.DID: switches}}
	m.discovery[d.id] = d
	cfg := saved.Config
	cfg.DiscoveryID = d.id
	return json.Marshal(cfg)
}

func (m *Manager) Update(ctx context.Context, id, name string, raw json.RawMessage) (c.Device, error) {
	cfg, err := parseConfig(raw)
	if err != nil {
		return c.Device{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.targets[id]
	if t == nil {
		return c.Device{}, c.Fail("DEVICE_NOT_FOUND", "米家设备不存在")
	}
	if !t.busy.CompareAndSwap(false, true) {
		return c.Device{}, c.Fail("RESOURCE_BUSY", "米家设备正在通信")
	}
	defer t.busy.Store(false)
	refreshID := cfg.DiscoveryID
	cfg.DiscoveryID = ""
	if cfg != t.saved.Config {
		return c.Device{}, c.Fail("CONFIG_CONFLICT", "已导入设备的账号与身份不能替换，请显式导入新设备并改绑项目")
	}
	var old c.Device
	if err := m.store.Get("devices", id, &old); err != nil {
		return c.Device{}, err
	}
	old.DisplayName = name
	if refreshID != "" {
		candidate, _, err := m.candidateLocked(refreshID, cfg.DID)
		if err != nil {
			return c.Device{}, err
		}
		if candidate.refreshDeviceID != id || candidate.accountID != cfg.AccountID || candidate.revision != old.Revision || len(candidate.prepared[cfg.DID]) == 0 {
			return c.Device{}, c.Fail("CONFIG_CONFLICT", "能力预览已变化，请重新刷新")
		}
		saved := t.saved
		saved.Switches = append([]Switch(nil), candidate.prepared[cfg.DID]...)
		old.Services = switchServices(saved.Switches)
		if err := m.store.Batch(storage.Change{Table: "xiaomi_devices", ID: id, Value: saved}, storage.Change{Table: "devices", ID: id, Value: old}); err != nil {
			return c.Device{}, err
		}
		t.saved = saved
		delete(m.discovery, refreshID)
	}
	return old, nil
}

func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.targets[id]
	if t == nil {
		return c.Fail("DEVICE_NOT_FOUND", "米家设备不存在")
	}
	if !t.busy.CompareAndSwap(false, true) {
		return c.Fail("RESOURCE_BUSY", "米家设备正在通信")
	}
	defer t.busy.Store(false)
	if err := m.store.Batch(storage.Change{Table: "xiaomi_devices", ID: id, Delete: true}, storage.Change{Table: "devices", ID: id, Delete: true}); err != nil {
		return err
	}
	delete(m.targets, id)
	return nil
}

func (m *Manager) acquire(id string) (*target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.targets[id]
	if t == nil || m.closed {
		return nil, c.Fail("DEVICE_OFFLINE", "米家设备执行路由不可用")
	}
	if time.Now().Before(t.quarantine) {
		return nil, c.Fail("RESOURCE_BUSY", "上次米家请求结果未知，仍在隔离窗口；请稍后显式读取状态")
	}
	if !t.busy.CompareAndSwap(false, true) {
		return nil, c.Fail("RESOURCE_BUSY", "米家设备正在通信，不排队")
	}
	return t, nil
}

func (m *Manager) Invoke(ctx context.Context, invocation c.Invocation) (c.Result, error) {
	t, err := m.acquire(invocation.DeviceID)
	if err != nil {
		return c.Result{}, err
	}
	defer t.busy.Store(false)
	var sw *Switch
	write := false
	for _, candidate := range t.saved.Switches {
		if candidate.Read && invocation.ServiceKey == candidate.key("read") || candidate.Write && invocation.ServiceKey == candidate.key("set") {
			v := candidate
			sw, write = &v, invocation.ServiceKey == candidate.key("set")
			break
		}
	}
	if sw == nil {
		return c.Result{}, c.Fail("BINDING_INVALID", "所选米家开关通道不可用")
	}
	service := switchServices([]Switch{*sw})
	for _, s := range service {
		if s.Key == invocation.ServiceKey {
			if err := s.Input.Validate(invocation.Parameters); err != nil {
				return c.Result{}, err
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	if !invocation.Deadline.IsZero() && invocation.Deadline.Before(deadline) {
		deadline = invocation.Deadline
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	cred, release, err := m.credentials(ctx, t.saved.Config.AccountID, false)
	var result c.Result
	if err == nil {
		defer release()
		var enabled *bool
		if write {
			v := invocation.Parameters["enabled"].(bool)
			enabled = &v
		}
		result, err = m.client.property(ctx, cred, t.saved.Config.DID, *sw, enabled)
		// 非认证的云端控制失败也不声称动作未发生。
		var remote *cloudError
		if write && errors.As(err, &remote) && !remote.auth {
			err = &c.Error{Code: "DEVICE_ERROR", Message: remote.Error(), Outcome: "unknown"}
		}
		err = m.accountError(t.saved.Config.AccountID, err)
	}
	// 云端缓存或网关接收确认不足以证明物理设备当前在线。
	state, message := "unknown", ""
	if err != nil {
		state, message = "offline", c.AsError(err).Message
		if c.AsError(err).Outcome == "unknown" {
			m.mu.Lock()
			t.quarantine = time.Now().Add(10 * time.Second)
			m.mu.Unlock()
			state = "unknown"
		}
	}
	// busy 包含上报，编辑和删除不会被旧状态覆盖。
	if reportErr := m.registrar.ReportAvailability(context.Background(), invocation.DeviceID, state, message); reportErr != nil && err == nil {
		return result, &c.Error{Code: "INTERNAL_ERROR", Message: "米家调用已完成，但设备状态保存失败，请按请求ID核查", Outcome: "unknown"}
	}
	return result, err
}

func (m *Manager) Test(ctx context.Context, id string) error {
	m.mu.Lock()
	t := m.targets[id]
	var key string
	if t != nil {
		for _, sw := range t.saved.Switches {
			if sw.Read {
				key = sw.key("read")
				break
			}
		}
	}
	m.mu.Unlock()
	if key == "" {
		return c.Fail("INVALID_ARGUMENT", "此设备没有可读取的开关通道，连接测试不会发送控制命令")
	}
	_, err := m.Invoke(ctx, c.Invocation{DeviceID: id, ServiceKey: key, Parameters: map[string]any{}, Deadline: time.Now().Add(10 * time.Second)})
	return err
}

var _ c.DeviceProvisioner = (*Manager)(nil)
var _ c.DeviceInvoker = (*Manager)(nil)
