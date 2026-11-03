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

func TestSharedBusAndConfigurationLifecycle(t *testing.T) {
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
	seen := make(chan string, 10)
	gate := make(chan struct{})
	m.Open = func(cfg transport.Config) (transport.Port, error) {
		a, b := net.Pipe()
		opt := testdevice.Options{Protocol: "modbus"}
		if cfg.Path == "/tmp/shared-bus" {
			opt.Seen = seen
			opt.Gate = gate
		}
		go testdevice.Serve(b, testdevice.NewState(), opt)
		return &testdevice.Port{Conn: a}, nil
	}
	cfg := func(addr int) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"path": "/tmp/shared-bus", "address": addr})
		return b
	}
	first, e := k.CreateDevice(context.Background(), "longwei-lw3010e", "从机1", cfg(1))
	if e != nil {
		t.Fatal(e)
	}
	second, e := k.CreateDevice(context.Background(), "longwei-lw3010e", "从机2", cfg(2))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = k.CreateDevice(context.Background(), "longwei-lw3010e", "重复", cfg(1)); e == nil {
		t.Fatal("重复串口地址未拒绝")
	}
	bad := json.RawMessage(`{"path":"/tmp/shared-bus","address":3,"baud_rate":19200}`)
	if _, e = k.CreateDevice(context.Background(), "longwei-lw3010e", "参数冲突", bad); e == nil {
		t.Fatal("总线参数冲突未拒绝")
	}
	done := make(chan error, 1)
	go func() {
		_, e := m.Invoke(context.Background(), c.Invocation{DeviceID: first.ID, ServiceKey: "settings.read"})
		done <- e
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("总线调用未开始")
	}
	_, e = m.Invoke(context.Background(), c.Invocation{DeviceID: second.ID, ServiceKey: "settings.read"})
	if c.AsError(e).Code != "RESOURCE_BUSY" {
		t.Fatal(e)
	}
	if _, e = m.Update(context.Background(), first.ID, "更名", cfg(1)); c.AsError(e).Code != "RESOURCE_BUSY" {
		t.Fatal("忙碌时更新未拒绝", e)
	}
	if e = m.Remove(context.Background(), first.ID); c.AsError(e).Code != "RESOURCE_BUSY" {
		t.Fatal("忙碌时删除未拒绝", e)
	}
	close(gate)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if e = m.Remove(context.Background(), first.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Invoke(context.Background(), c.Invocation{DeviceID: second.ID, ServiceKey: "settings.read"}); e != nil {
		t.Fatal("删除一个从机关闭了共享连接", e)
	}
}
func TestCanonicalAliasDuplicate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "serial")
	if e := os.WriteFile(target, nil, 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "by-id")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	a, b := transport.Config{Path: target}, transport.Config{Path: link}
	if e := a.Normalize(); e != nil {
		t.Fatal(e)
	}
	if e := b.Normalize(); e != nil {
		t.Fatal(e)
	}
	if a.Path == b.Path || b.Path != link {
		t.Fatal("稳定路径被解析结果覆盖")
	}
	if transport.EndpointPath(a.Path) != transport.EndpointPath(b.Path) {
		t.Fatal("别名没有定位到相同共享资源")
	}
}

func TestItechIdentitySurvivesRestartAndRejectsReplacement(t *testing.T) {
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	k := core.New(s)
	first := manager.New(s, k)
	k.Provisioner = first
	k.Invoker = first
	opener := func(identity string, state *testdevice.State) transport.Opener {
		return func(transport.Config) (transport.Port, error) {
			a, b := net.Pipe()
			go testdevice.Serve(b, state, testdevice.Options{ReplyOverride: map[string]string{"*IDN?": "ITECH Ltd., IT6722, " + identity + ", 1.0"}})
			return &testdevice.Port{Conn: a}, nil
		}
	}
	first.Open = opener("ORIGINAL", testdevice.NewState())
	d, e := k.CreateDevice(context.Background(), "itech-it6722", "原设备", json.RawMessage(`{"path":"/tmp/identity-port"}`))
	if e != nil {
		t.Fatal(e)
	}
	if e = first.Test(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	first.Close()
	replacement := testdevice.NewState()
	second := manager.New(s, k)
	defer second.Close()
	second.Open = opener("REPLACEMENT", replacement)
	k.Provisioner = second
	k.Invoker = second
	if e = second.Restore(context.Background()); e != nil {
		t.Fatal(e)
	}
	_, e = second.Invoke(context.Background(), c.Invocation{DeviceID: d.ID, ServiceKey: "output.set", Parameters: map[string]any{"enabled": true}})
	if e == nil || c.AsError(e).Code != "DEVICE_ERROR" {
		t.Fatal("同型号替换设备未被拒绝", e)
	}
	_, _, _, writes := replacement.Snapshot()
	if writes != 0 {
		t.Fatal("向身份不匹配设备发送了控制命令")
	}
}

type reportingRegistrar struct {
	started chan struct{}
	finish  chan struct{}
}

func (r *reportingRegistrar) UpsertDevice(context.Context, c.Device) error { return nil }
func (r *reportingRegistrar) ReportAvailability(context.Context, string, string, string) error {
	close(r.started)
	<-r.finish
	return nil
}
func TestConfigurationCannotOvertakeAvailabilityReport(t *testing.T) {
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	reporter := &reportingRegistrar{started: make(chan struct{}), finish: make(chan struct{})}
	m := manager.New(s, reporter)
	defer m.Close()
	m.Open = func(transport.Config) (transport.Port, error) {
		a, b := net.Pipe()
		go testdevice.Serve(b, testdevice.NewState(), testdevice.Options{})
		return &testdevice.Port{Conn: a}, nil
	}
	raw := json.RawMessage(`{"path":"/tmp/report-race"}`)
	d, e := m.Create(context.Background(), "itech-it6722", "状态竞态", raw)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := m.Invoke(context.Background(), c.Invocation{DeviceID: d.ID, ServiceKey: "settings.read"})
		done <- e
	}()
	select {
	case <-reporter.started:
	case <-time.After(time.Second):
		t.Fatal("未进入状态上报")
	}
	_, e = m.Update(context.Background(), d.ID, "新连接", json.RawMessage(`{"path":"/tmp/new-report-race"}`))
	if e == nil || c.AsError(e).Code != "RESOURCE_BUSY" {
		close(reporter.finish)
		<-done
		t.Fatal("状态报告完成前允许了配置更新", e)
	}
	close(reporter.finish)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = m.Update(context.Background(), d.ID, "新连接", json.RawMessage(`{"path":"/tmp/new-report-race"}`)); e != nil {
		t.Fatal("状态报告结束后未允许更新", e)
	}
}
