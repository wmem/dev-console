package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"devconsole/internal/access/power"
	"devconsole/internal/access/transport"
	"devconsole/internal/auth"
	c "devconsole/internal/contracts"
	"devconsole/internal/storage"
)

type Saved struct {
	ID          string                 `json:"id"`
	Template    string                 `json:"template_key"`
	Config      transport.Config       `json:"connection_config"`
	Identity    string                 `json:"identity,omitempty"`
	USBIdentity *transport.USBIdentity `json:"usb_identity,omitempty"`
}
type target struct {
	saved Saved
	bus   *transport.Bus
}
type Manager struct {
	store     *storage.Store
	registrar c.DeviceRegistrar
	mu        sync.Mutex
	targets   map[string]target
	buses     map[string]*transport.Bus
	Open      transport.Opener
	Trace     func(string, []byte)
	Ports     func() ([]transport.PortInfo, error)
}

func New(s *storage.Store, r c.DeviceRegistrar) *Manager {
	return &Manager{store: s, registrar: r, targets: map[string]target{}, buses: map[string]*transport.Bus{}}
}
func (m *Manager) ListTemplates(ctx context.Context) []c.Template {
	path := c.Schema{Type: "string", Description: "Linux实际串口路径"}
	baud := c.Num(300, 921600, 1, "")
	baud.Default = 9600.0
	bits := c.Num(5, 8, 1, "")
	bits.Default = 8.0
	parity := c.Schema{Type: "string", Enum: []any{"none", "even", "odd"}, Default: "none"}
	stop := c.Num(1, 2, 1, "")
	stop.Default = 1.0
	timeout := c.Num(100, 10000, 1, "ms")
	timeout.Default = 1500.0
	out := []c.Template{}
	for _, key := range []string{"itech-it6722", "longwei-lw3010e"} {
		v, i := 80.0, 20.0
		title := "ITECH IT6722"
		if key == "longwei-lw3010e" {
			v, i = 30, 10
			title = "龙威 LW-3010E"
		}
		vs, is := c.Num(.01, v, .01, "V"), c.Num(.01, i, .01, "A")
		vs.Default = v
		is.Default = i
		props := map[string]c.Schema{"path": path, "baud_rate": baud, "data_bits": bits, "parity": parity, "stop_bits": stop, "timeout_ms": timeout, "max_voltage": vs, "max_current": is}
		if key == "longwei-lw3010e" {
			a := c.Num(1, 247, 1, "")
			a.Default = 1.0
			props["address"] = a
		}
		out = append(out, c.Template{Key: key, Title: title, ConfigSchema: c.Object(props, "path"), ConnectionField: "path"})
	}
	return out
}
func (m *Manager) parse(key string, raw json.RawMessage) (transport.Config, error) {
	var schema c.Schema
	found := false
	for _, t := range m.ListTemplates(context.Background()) {
		if t.Key == key {
			schema = t.ConfigSchema
			found = true
			break
		}
	}
	if !found {
		return transport.Config{}, c.Fail("INVALID_ARGUMENT", "未知接入模板")
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return transport.Config{}, c.Fail("INVALID_ARGUMENT", "连接配置需为JSON对象")
	}
	for k, s := range schema.Properties {
		if _, ok := obj[k]; !ok && s.Default != nil {
			obj[k] = s.Default
		}
	}
	if e := schema.Validate(obj); e != nil {
		return transport.Config{}, e
	}
	b, _ := json.Marshal(obj)
	var cfg transport.Config
	_ = json.Unmarshal(b, &cfg)
	if e := cfg.Normalize(); e != nil {
		return cfg, e
	}
	return cfg, nil
}
func (m *Manager) check(cfg transport.Config, key, except string) error {
	for id, t := range m.targets {
		if id == except || transport.EndpointPath(t.saved.Config.Path) != transport.EndpointPath(cfg.Path) {
			continue
		}
		if key != "longwei-lw3010e" || t.saved.Template != key {
			return c.Fail("CONFIG_CONFLICT", "该串口已用于独占设备")
		}
		peer := t.saved.Config
		peer.Path = transport.EndpointPath(peer.Path)
		resolved := cfg
		resolved.Path = transport.EndpointPath(resolved.Path)
		if !resolved.SameBus(peer) {
			return c.Fail("CONFIG_CONFLICT", "共享总线串口参数必须一致")
		}
		if cfg.Address == t.saved.Config.Address {
			return c.Fail("CONFIG_CONFLICT", "同一串口与从机地址不能重复")
		}
	}
	return nil
}
func (m *Manager) device(s Saved, name string) c.Device {
	return c.Device{ID: s.ID, TemplateKey: s.Template, DisplayName: name, AccessRoute: "builtin-access-1", Availability: "offline", LastError: "尚未完成连接探测", Services: power.Services(s.Config.MaxVoltage, s.Config.MaxCurrent), Revision: 1}
}

// 注册配置与运行连接分离，只有调用时才按当前实际节点获取共享Bus。
func (m *Manager) attach(s Saved) { m.targets[s.ID] = target{saved: s} }
func (m *Manager) Create(ctx context.Context, key, name string, raw json.RawMessage) (c.Device, error) {
	cfg, e := m.parse(key, raw)
	if e != nil {
		return c.Device{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e = m.check(cfg, key, ""); e != nil {
		return c.Device{}, e
	}
	identity, e := m.bindingIdentity(cfg, nil, true)
	if e != nil {
		return c.Device{}, e
	}
	s := Saved{ID: auth.ID(), Template: key, Config: cfg, USBIdentity: identity}
	d := m.device(s, name)
	if e = m.store.Batch(storage.Change{Table: "access_configs", ID: s.ID, Value: s}, storage.Change{Table: "devices", ID: s.ID, Value: d}); e != nil {
		return c.Device{}, e
	}
	m.attach(s)
	return d, nil
}
func (m *Manager) Config(ctx context.Context, id string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok {
		return nil, c.Fail("DEVICE_OFFLINE", "接入实例不存在")
	}
	b, e := json.Marshal(t.saved.Config)
	return b, e
}
func (m *Manager) Update(ctx context.Context, id, name string, raw json.RawMessage) (c.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.targets[id]
	if !ok {
		return c.Device{}, c.Fail("DEVICE_OFFLINE", "接入实例不存在")
	}
	cfg, e := m.parse(old.saved.Template, raw)
	if e != nil {
		return c.Device{}, e
	}
	if e = m.check(cfg, old.saved.Template, id); e != nil {
		return c.Device{}, e
	}
	oldBus := old.bus
	if oldBus == nil {
		oldBus = m.buses[transport.EndpointPath(old.saved.Config.Path)]
	}
	if oldBus != nil {
		if !oldBus.Acquire() {
			return c.Device{}, c.Fail("RESOURCE_BUSY", "设备通信正在进行")
		}
		defer oldBus.Release()
	}
	if newBus, ok := m.buses[transport.EndpointPath(cfg.Path)]; ok && newBus != oldBus {
		if !newBus.Acquire() {
			return c.Device{}, c.Fail("RESOURCE_BUSY", "目标串口正在通信")
		}
		defer newBus.Release()
	}
	var expected *transport.USBIdentity
	if cfg.Path == old.saved.Config.Path {
		expected = old.saved.USBIdentity
	}
	identity, e := m.bindingIdentity(cfg, expected, true)
	if e != nil {
		return c.Device{}, e
	}
	s := Saved{ID: id, Template: old.saved.Template, Config: cfg, Identity: old.saved.Identity, USBIdentity: identity}
	d := m.device(s, name)
	var previous c.Device
	if e = m.store.Get("devices", id, &previous); e != nil {
		return d, e
	}
	d.Revision = previous.Revision + 1
	if e = m.store.Batch(storage.Change{Table: "access_configs", ID: id, Value: s}, storage.Change{Table: "devices", ID: id, Value: d}); e != nil {
		return c.Device{}, e
	}
	m.detachHeld(id, old)
	m.attach(s)
	return d, nil
}
func (m *Manager) detachHeld(id string, old target) {
	delete(m.targets, id)
	if old.bus == nil {
		return
	}
	used := false
	for _, v := range m.targets {
		if v.bus == old.bus {
			used = true
		}
	}
	if !used {
		old.bus.RetireHeld()
		delete(m.buses, old.bus.Config.Path)
	}
}
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.targets[id]
	if !ok {
		return m.store.Batch(storage.Change{Table: "access_configs", ID: id, Delete: true}, storage.Change{Table: "devices", ID: id, Delete: true})
	}
	oldBus := old.bus
	if oldBus == nil {
		oldBus = m.buses[transport.EndpointPath(old.saved.Config.Path)]
	}
	if oldBus != nil {
		if !oldBus.Acquire() {
			return c.Fail("RESOURCE_BUSY", "设备通信正在进行")
		}
		defer oldBus.Release()
	}
	if e := m.store.Batch(storage.Change{Table: "access_configs", ID: id, Delete: true}, storage.Change{Table: "devices", ID: id, Delete: true}); e != nil {
		return e
	}
	m.detachHeld(id, old)
	return nil
}
func (m *Manager) Restore(ctx context.Context) error {
	all, e := storage.List[Saved](m.store, "access_configs")
	if e != nil {
		return e
	}
	for _, s := range all {
		var d c.Device
		if e = m.store.Get("devices", s.ID, &d); e != nil {
			return fmt.Errorf("接入配置缺少设备登记：%s", s.ID)
		}
		m.mu.Lock()
		// 启动后节点可能已变化；保留配置供管理员修复，冲突在探测/调用时拒绝，不能阻断平台启动。
		m.attach(s)
		m.mu.Unlock()
		d.TemplateKey = s.Template
		d.Services = power.Services(s.Config.MaxVoltage, s.Config.MaxCurrent)
		d.Availability = "offline"
		if e = m.registrar.UpsertDevice(ctx, d); e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) acquire(id string) (target, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok {
		return t, c.Fail("DEVICE_OFFLINE", "设备执行路由不可用")
	}
	if e := m.check(t.saved.Config, t.saved.Template, id); e != nil {
		return t, e
	}
	identity, e := m.bindingIdentity(t.saved.Config, t.saved.USBIdentity)
	if e != nil {
		return t, e
	}
	endpoint := transport.EndpointPath(t.saved.Config.Path)
	if t.bus != nil && t.bus.Config.Path != endpoint {
		if !t.bus.Acquire() {
			return t, c.Fail("RESOURCE_BUSY", "原串口通信仍在执行，暂不能迁移连接")
		}
		old := t
		m.detachHeld(id, old)
		old.bus.Release()
		t.bus = nil
		m.targets[id] = t
	}
	// 清理仍持有本节点但稳定路径已指向别处的空闲连接，不能把新请求塞进旧设备的句柄。
	if bus, exists := m.buses[endpoint]; exists {
		stale := map[string]target{}
		for other, v := range m.targets {
			if v.bus == bus && transport.EndpointPath(v.saved.Config.Path) != endpoint {
				stale[other] = v
			}
		}
		if len(stale) > 0 {
			if !bus.Acquire() {
				return t, c.Fail("RESOURCE_BUSY", "原设备仍在使用该节点，暂不能重连")
			}
			for other, v := range stale {
				m.detachHeld(other, v)
				v.bus = nil
				m.targets[other] = v
			}
			bus.Release()
		}
	}
	resolved := t.saved.Config
	resolved.Path = endpoint
	bus, exists := m.buses[endpoint]
	if exists && !bus.Config.SameBus(resolved) {
		return t, c.Fail("CONFIG_CONFLICT", "实际串口的连接参数存在冲突")
	}
	if !exists {
		bus = transport.New(resolved, m.Open)
	}
	if !bus.Acquire() {
		return t, c.Fail("RESOURCE_BUSY", "串口正在执行其他通信")
	}
	if identity != nil && t.saved.USBIdentity == nil {
		t.saved.USBIdentity = identity
		if e = m.store.Put("access_configs", id, t.saved); e != nil {
			bus.Release()
			return t, e
		}
	}
	t.bus = bus
	m.buses[endpoint] = bus
	m.targets[id] = t
	return t, nil
}
func (m *Manager) Test(ctx context.Context, id string) error {
	_, e := m.Invoke(ctx, c.Invocation{DeviceID: id, ServiceKey: "settings.read", Parameters: map[string]any{}, Deadline: time.Now().Add(5 * time.Second)})
	return e
}
func (m *Manager) Invoke(ctx context.Context, r c.Invocation) (c.Result, error) {
	t, e := m.acquire(r.DeviceID)
	if e != nil {
		if code := c.AsError(e).Code; code == "CONFIG_CONFLICT" || code == "DEVICE_OFFLINE" {
			_ = m.registrar.ReportAvailability(context.Background(), r.DeviceID, "offline", e.Error())
		}
		return c.Result{}, e
	}
	// 状态报告属于本次连接生命周期，完成前不能让配置更新或删除越过此调用。
	defer t.bus.Release()
	var out c.Result
	sent, e := t.bus.RunHeld(ctx, func(w *transport.Wire) error {
		// 打开句柄后、发送任何报文前再核验，避免解析与打开之间发生热插拔或链接改指向。
		if transport.EndpointPath(t.saved.Config.Path) != t.bus.Config.Path {
			return c.Fail("CONFIG_CONFLICT", "串口稳定路径在连接过程中发生变化，请重新调用")
		}
		if _, e := m.bindingIdentity(t.saved.Config, t.saved.USBIdentity); e != nil {
			return e
		}
		var err error
		if t.saved.Template == "itech-it6722" {
			identity, identityErr := power.ReadIdentity(w)
			if identityErr != nil {
				return identityErr
			}
			if t.saved.Identity != "" && identity != t.saved.Identity {
				return c.Fail("DEVICE_ERROR", "设备序列号与首次确认的身份不一致，请明确登记新设备并重新绑定")
			}
			if t.saved.Identity == "" {
				m.mu.Lock()
				updated := m.targets[r.DeviceID]
				updated.saved.Identity = identity
				identityErr = m.store.Put("access_configs", r.DeviceID, updated.saved)
				if identityErr == nil {
					m.targets[r.DeviceID] = updated
				}
				m.mu.Unlock()
				if identityErr != nil {
					return identityErr
				}
			}
			out, err = power.Itech(w, t.saved.Config, r.ServiceKey, r.Parameters)
		} else {
			out, err = power.Longwei(w, t.saved.Config, r.ServiceKey, r.Parameters)
		}
		return err
	}, m.Trace)
	e = transport.MapError(e, sent)
	state, msg := "online", ""
	if e != nil {
		state, msg = "offline", e.Error()
	} else if transport.EndpointPath(t.saved.Config.Path) != t.bus.Config.Path {
		state, msg = "offline", "稳定路径已变化，本次结果来自开始时的连接；下次调用将重新定位"
	}
	if re := m.registrar.ReportAvailability(context.Background(), r.DeviceID, state, msg); re != nil && e == nil {
		return out, re
	}
	return out, e
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buses {
		if b.Acquire() {
			b.RetireHeld()
			b.Release()
		}
	}
}

// FinishItechTest仅供显式硬件测试工具交还本地控制，普通调用和服务退出均不触发。
func (m *Manager) FinishItechTest(ctx context.Context, id string) error {
	t, e := m.acquire(id)
	if e != nil {
		return e
	}
	defer t.bus.Release()
	if t.saved.Template != "itech-it6722" {
		return c.Fail("INVALID_ARGUMENT", "设备不是IT6722")
	}
	sent, e := t.bus.RunHeld(ctx, func(w *transport.Wire) error { return w.Write([]byte("SYST:LOC\n"), true) }, m.Trace)
	return transport.MapError(e, sent)
}
