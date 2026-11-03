package manager_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/access/transport"
	c "devconsole/internal/contracts"
	"devconsole/internal/core"
	"devconsole/internal/storage"
	"devconsole/internal/testdevice"
)

func TestStableAliasReconnectAndRejectDuplicate(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "ttyUSB0"), filepath.Join(dir, "ttyUSB1")
	link := filepath.Join(dir, "serial", "by-path", "fixed-socket")
	if e := os.MkdirAll(filepath.Dir(link), 0755); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{a, b} {
		if e := os.WriteFile(p, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Symlink(a, link); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Provisioner = m
	k.Invoker = m
	opened := []string{}
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		opened = append(opened, cfg.Path)
		u, v := net.Pipe()
		go testdevice.Serve(v, testdevice.NewState(), testdevice.Options{})
		return &testdevice.Port{Conn: u}, nil
	}
	raw, _ := json.Marshal(map[string]any{"path": link})
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "稳定路径", raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	var saved manager.Saved
	if e = s.Get("access_configs", d.ID, &saved); e != nil || saved.Config.Path != link {
		t.Fatal("稳定路径没有持久保留", saved, e)
	}
	duplicate, _ := json.Marshal(map[string]any{"path": a})
	if _, e = k.CreateDevice(context.Background(), "itech-it6722", "同端口别名", duplicate); e == nil {
		t.Fatal("别名绕过了重复串口检查")
	}
	if e = os.Remove(link); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(b, link); e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	if len(opened) != 2 || opened[0] != a || opened[1] != b {
		t.Fatal("编号变化未重新打开正确节点", opened)
	}
	current, e := k.Device(d.ID)
	if e != nil || current.ID != d.ID {
		t.Fatal("迁移连接改变了设备身份", current, e)
	}
	m.Close()
	fresh := manager.New(s, k)
	defer fresh.Close()
	fresh.Open = m.Open
	k.Provisioner = fresh
	k.Invoker = fresh
	if e = fresh.Restore(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = fresh.Test(context.Background(), d.ID); e != nil {
		t.Fatal("重启未恢复稳定选择", e)
	}
	if opened[len(opened)-1] != b {
		t.Fatal(opened)
	}
}
func TestUSBChoicesAndIdentityValidation(t *testing.T) {
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Provisioner = m
	k.Invoker = m
	root := t.TempDir()
	node := filepath.Join(root, "ttyACM0")
	alias := filepath.Join(root, "serial", "by-id", "unique")
	socket := filepath.Join(root, "serial", "by-path", "socket")
	if e = os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(socket), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(node, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(node, alias); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(node, socket); e != nil {
		t.Fatal(e)
	}
	info := transport.PortInfo{Path: node, USB: transport.USBIdentity{VID: "1234", PID: "5678", Interface: "00"}, ByID: []string{alias}, ByPath: []string{socket}}
	ports := []transport.PortInfo{info}
	m.Ports = func() ([]transport.PortInfo, error) { return ports, nil }
	choices, e := m.DiscoverConnections(context.Background(), "itech-it6722")
	if e != nil || len(choices) != 3 || choices[0].Selectable || !choices[1].Selectable {
		t.Fatal("无序列号设备被当成唯一绑定", choices, e)
	}
	raw, _ := json.Marshal(map[string]any{"path": alias})
	if _, e = k.CreateDevice(context.Background(), "itech-it6722", "无唯一身份", raw); e == nil {
		t.Fatal("无序列号by-id未拒绝")
	}
	ports[0].USB.Serial = "UNIQUE"
	m.Open = func(transport.Config) (transport.Port, error) {
		t.Fatal("身份不符时不应打开串口")
		return nil, nil
	}
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "唯一身份", raw)
	if e != nil {
		t.Fatal(e)
	}
	second := ports[0]
	second.Path = filepath.Join(root, "ttyACM1")
	ports = append(ports, second)
	if e = m.Test(context.Background(), d.ID); e == nil || c.AsError(e).Code != "CONFIG_CONFLICT" {
		t.Fatal("重复USB序列号未拒绝", e)
	}
	ports = ports[:1]
	ports[0].USB.Serial = "OTHER"
	if e = m.Test(context.Background(), d.ID); e == nil || c.AsError(e).Code != "CONFIG_CONFLICT" {
		t.Fatal("USB身份变化未拒绝", e)
	}
	ports = nil
	if _, e = m.Update(context.Background(), d.ID, "离线改名", raw); e != nil {
		t.Fatal("离线编辑不应丢弃已有USB身份", e)
	}
	var saved manager.Saved
	if e = s.Get("access_configs", d.ID, &saved); e != nil || saved.USBIdentity == nil || saved.USBIdentity.Serial != "UNIQUE" {
		t.Fatal(saved, e)
	}
	if e = m.Test(context.Background(), d.ID); e == nil || c.AsError(e).Code != "DEVICE_OFFLINE" {
		t.Fatal("离线身份未核验却尝试连接", e)
	}
}

func TestAliasMoveBusyAndIndependentTargetSnapshot(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	link := filepath.Join(dir, "alias")
	for _, p := range []string{a, b} {
		if e := os.WriteFile(p, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Symlink(a, link); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Provisioner = m
	k.Invoker = m
	seen := make(chan string, 8)
	gate := make(chan struct{})
	opened := []string{}
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		opened = append(opened, cfg.Path)
		x, y := net.Pipe()
		opt := testdevice.Options{}
		if cfg.Path == a {
			opt.Seen = seen
			opt.Gate = gate
		}
		go testdevice.Serve(y, testdevice.NewState(), opt)
		return &testdevice.Port{Conn: x}, nil
	}
	raw, _ := json.Marshal(map[string]any{"path": link})
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "重连快照", raw)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- m.Test(context.Background(), d.ID) }()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("调用未开始")
	}
	if e = os.Remove(link); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(b, link); e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e == nil || c.AsError(e).Code != "RESOURCE_BUSY" {
		t.Fatal("链接变化抢占了正在执行的通信", e)
	}
	close(gate)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	if len(opened) != 2 || opened[0] != a || opened[1] != b {
		t.Fatal(opened)
	}
}

func TestReselectKeepsInstrumentSerialGuard(t *testing.T) {
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Provisioner = m
	k.Invoker = m
	replacement := testdevice.NewState()
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		a, b := net.Pipe()
		serial := "ORIGINAL"
		state := testdevice.NewState()
		if cfg.Path == "/tmp/reselect-other" {
			serial = "REPLACEMENT"
			state = replacement
		}
		go testdevice.Serve(b, state, testdevice.Options{ReplyOverride: map[string]string{"*IDN?": "ITECH Ltd., IT6722, " + serial + ", 1.0"}})
		return &testdevice.Port{Conn: a}, nil
	}
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "原仪器", json.RawMessage(`{"path":"/tmp/reselect-original"}`))
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = k.UpdateDevice(context.Background(), d.ID, d.DisplayName, json.RawMessage(`{"path":"/tmp/reselect-other"}`), d.Revision); e != nil {
		t.Fatal(e)
	}
	_, e = m.Invoke(context.Background(), c.Invocation{DeviceID: d.ID, ServiceKey: "output.set", Parameters: map[string]any{"enabled": true}})
	if e == nil || c.AsError(e).Code != "DEVICE_ERROR" {
		t.Fatal("重选串口清除了仪器身份校验", e)
	}
	_, _, _, writes := replacement.Snapshot()
	if writes != 0 {
		t.Fatal("对替换仪器发送了控制命令")
	}
}

func TestUniqueUSBIdentityFollowsRenumberedACM(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "ttyACM0"), filepath.Join(dir, "ttyACM3")
	alias := filepath.Join(dir, "serial", "by-id", "unique-interface00")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{a, b} {
		if e := os.WriteFile(p, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Symlink(a, alias); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Invoker = m
	k.Provisioner = m
	current := a
	usb := transport.USBIdentity{VID: "1234", PID: "5678", Serial: "UNIQUE-BOARD", Interface: "00"}
	m.Ports = func() ([]transport.PortInfo, error) {
		return []transport.PortInfo{{Path: current, USB: usb, ByID: []string{alias}}}, nil
	}
	opened := []string{}
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		opened = append(opened, cfg.Path)
		x, y := net.Pipe()
		go testdevice.Serve(y, testdevice.NewState(), testdevice.Options{})
		return &testdevice.Port{Conn: x}, nil
	}
	raw, _ := json.Marshal(map[string]any{"path": alias})
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "ACM设备", raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(alias); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(b, alias); e != nil {
		t.Fatal(e)
	}
	current = b
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal("ACM编号变化后原USB身份未跟随", e)
	}
	if len(opened) != 2 || opened[1] != b {
		t.Fatal(opened)
	}
	var saved manager.Saved
	if e = s.Get("access_configs", d.ID, &saved); e != nil || saved.Config.Path != alias || saved.USBIdentity == nil || *saved.USBIdentity != usb {
		t.Fatal(saved, e)
	}
}

func TestUSBReplacementBetweenResolutionAndOpenIsRejected(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "ttyACM0")
	alias := filepath.Join(dir, "serial", "by-id", "unique")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(node, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(node, alias); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Invoker = m
	k.Provisioner = m
	serial := "ORIGINAL"
	m.Ports = func() ([]transport.PortInfo, error) {
		return []transport.PortInfo{{Path: node, USB: transport.USBIdentity{VID: "1234", PID: "5678", Serial: serial}, ByID: []string{alias}}}, nil
	}
	replacement := testdevice.NewState()
	m.Open = func(transport.Config) (transport.Port, error) {
		serial = "REPLACEMENT"
		a, b := net.Pipe()
		go testdevice.Serve(b, replacement, testdevice.Options{})
		return &testdevice.Port{Conn: a}, nil
	}
	raw, _ := json.Marshal(map[string]any{"path": alias})
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "打开期间替换", raw)
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.Invoke(context.Background(), c.Invocation{DeviceID: d.ID, ServiceKey: "output.set", Parameters: map[string]any{"enabled": true}})
	if e == nil || c.AsError(e).Code != "CONFIG_CONFLICT" {
		t.Fatal("打开期间的USB身份变化未拒绝", e)
	}
	_, _, _, writes := replacement.Snapshot()
	if writes != 0 {
		t.Fatal("USB身份变化后仍发送了控制")
	}
}

func TestRestartWithConvergedAliasesCanBeReconfigured(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	one, two := filepath.Join(dir, "one"), filepath.Join(dir, "two")
	for _, p := range []string{a, b} {
		if e := os.WriteFile(p, nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Symlink(a, one); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(b, two); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	k.Provisioner = m
	k.Invoker = m
	raw := func(p string) json.RawMessage { v, _ := json.Marshal(map[string]any{"path": p}); return v }
	first, e := k.CreateDevice(context.Background(), "itech-it6722", "一号", raw(one))
	if e != nil {
		t.Fatal(e)
	}
	second, e := k.CreateDevice(context.Background(), "itech-it6722", "二号", raw(two))
	if e != nil {
		t.Fatal(e)
	}
	m.Close()
	if e := os.Remove(two); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(a, two); e != nil {
		t.Fatal(e)
	}
	fresh := manager.New(s, k)
	defer fresh.Close()
	k.Provisioner = fresh
	k.Invoker = fresh
	fresh.Open = func(transport.Config) (transport.Port, error) {
		x, y := net.Pipe()
		go testdevice.Serve(y, testdevice.NewState(), testdevice.Options{})
		return &testdevice.Port{Conn: x}, nil
	}
	if e = fresh.Restore(context.Background()); e != nil {
		t.Fatal("动态别名冲突阻止了平台恢复", e)
	}
	if e = fresh.Test(context.Background(), first.ID); e == nil || c.AsError(e).Code != "CONFIG_CONFLICT" {
		t.Fatal("动态冲突未拒绝执行", e)
	}
	if _, e = k.UpdateDevice(context.Background(), second.ID, second.DisplayName, raw(b), second.Revision); e != nil {
		t.Fatal("冲突设备不能通过改串口修复", e)
	}
	if e = fresh.Test(context.Background(), first.ID); e != nil {
		t.Fatal(e)
	}
	if e = fresh.Test(context.Background(), second.ID); e != nil {
		t.Fatal(e)
	}
}

func TestOfflineByIDRequiresIdentityBeforeFirstOpen(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "ttyACM0")
	alias := filepath.Join(dir, "serial", "by-id", "offline")
	if e := os.MkdirAll(filepath.Dir(alias), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(node, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(node, alias); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	defer m.Close()
	k.Provisioner = m
	k.Invoker = m
	ports := []transport.PortInfo{}
	m.Ports = func() ([]transport.PortInfo, error) { return ports, nil }
	m.Open = func(transport.Config) (transport.Port, error) {
		t.Fatal("未知USB身份时不应打开端点")
		return nil, nil
	}
	raw, _ := json.Marshal(map[string]any{"path": alias})
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "离线稳定路径", raw)
	if e != nil {
		t.Fatal("离线配置应允许保存", e)
	}
	if e = m.Test(context.Background(), d.ID); e == nil || c.AsError(e).Code != "DEVICE_OFFLINE" {
		t.Fatal("未获得唯一USB身份却开始连接", e)
	}
	ports = append(ports, transport.PortInfo{Path: node, USB: transport.USBIdentity{VID: "1234", PID: "5678", Serial: "FIRST-VERIFIED"}, ByID: []string{alias}})
	m.Open = func(transport.Config) (transport.Port, error) {
		x, y := net.Pipe()
		go testdevice.Serve(y, testdevice.NewState(), testdevice.Options{})
		return &testdevice.Port{Conn: x}, nil
	}
	if e = m.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	var saved manager.Saved
	if e = s.Get("access_configs", d.ID, &saved); e != nil || saved.USBIdentity == nil || saved.USBIdentity.Serial != "FIRST-VERIFIED" {
		t.Fatal(saved, e)
	}
}
