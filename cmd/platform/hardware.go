package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/auth"
	"devconsole/internal/core"
	"devconsole/internal/storage"
)

func hardwareTest(args []string) (ret error) {
	f := flag.NewFlagSet("hardware-test", flag.ContinueOnError)
	port := f.String("port", "/dev/ttyUSB0", "实际串口")
	device := f.String("device", "itech-it6722", "设备模板：itech-it6722或longwei-lw3010e")
	address := f.Int("address", 1, "龙威Modbus从机地址")
	readOnly := f.Bool("read-only", false, "只查询设定值、输出状态与实测值，不修改设备")
	report := f.String("report", "", "记录路径，默认按设备类型生成")
	run := f.Bool("run", false, "显式执行已授权的空载控制测试")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *device != "itech-it6722" && *device != "longwei-lw3010e" {
		return errors.New("未知设备模板")
	}
	if *address < 1 || *address > 247 {
		return errors.New("从机地址需为1至247")
	}
	if *report == "" {
		if *device == "itech-it6722" {
			*report = "./docs/evidence/itech-hardware.json"
		} else {
			*report = "./docs/evidence/longwei-hardware.json"
		}
		if *readOnly {
			*report = filepath.Join(filepath.Dir(*report), strings.TrimSuffix(filepath.Base(*report), ".json")+"-readonly.json")
		}
	}
	if !*run && !*readOnly {
		return errors.New("需显式传入--run执行空载控制测试")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	callContext := ctx
	dir, e := os.MkdirTemp("", "devconsole-hardware-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	s, e := storage.Open(dir)
	if e != nil {
		return e
	}
	defer s.Close()
	k := core.New(s)
	m := manager.New(s, k)
	k.Invoker = m
	k.Provisioner = m
	defer m.Close()
	events := []map[string]any{}
	records := []core.Record{}
	recovery := map[string]any{"attempted": false, "confirmed": false}
	m.Trace = func(direction string, b []byte) {
		events = append(events, map[string]any{"at": time.Now().UTC(), "direction": direction, "hex": hex.EncodeToString(b), "text": string(b)})
	}
	defer func() {
		name := "IT6722"
		if *device == "longwei-lw3010e" {
			name = "LW-3010E"
		}
		out := map[string]any{"device": name, "template_key": *device, "read_only": *readOnly, "port": *port, "scope": "空载设置与回读，不含带载限流或外部仪表精度验证", "finished_at": time.Now().UTC(), "records": records, "wire_events": events, "recovery": recovery, "passed": ret == nil}
		if *readOnly {
			out["scope"] = "只读查询设定值、输出状态与实测值，不修改设备"
		}
		if *device == "longwei-lw3010e" {
			out["address"] = *address
		}
		if ret != nil {
			out["error"] = ret.Error()
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		if e := os.MkdirAll(filepath.Dir(*report), 0755); e != nil {
			ret = errors.Join(ret, e)
			return
		}
		if e := os.WriteFile(*report, b, 0600); e != nil {
			ret = errors.Join(ret, e)
		}
	}()
	config := map[string]any{"path": *port}
	if *device == "longwei-lw3010e" {
		config["address"] = *address
	}
	raw, _ := json.Marshal(config)
	d, e := k.CreateDevice(context.Background(), *device, "实机验收", raw)
	if e != nil {
		return e
	}
	p := auth.Principal{ID: "hardware-test", Kind: "user", Role: "admin"}
	call := func(key string, params map[string]any) (core.Record, error) {
		r, e := k.InvokeDevice(callContext, p, d.ID, key, params)
		records = append(records, r)
		if e != nil {
			return r, e
		}
		fmt.Println("已验证", key, r.Confirmation)
		return r, nil
	}
	settings, e := call("settings.read", map[string]any{})
	if e != nil {
		return e
	}
	state, e := call("output.read", map[string]any{})
	if e != nil {
		return e
	}
	if _, e = call("measurement.read", map[string]any{}); e != nil {
		return e
	}
	if *readOnly {
		return nil
	}
	originalV := settings.Result["voltage"].(float64)
	originalI := settings.Result["current_limit"].(float64)
	originalOn := state.Result["enabled"].(bool)
	// 初始值必须能经同一执行链恢复；否则在任何写入前结束，不能静默放宽设备边界。
	for _, service := range d.Services {
		var original map[string]any
		switch service.Key {
		case "voltage.set":
			original = map[string]any{"voltage": originalV}
		case "current_limit.set":
			original = map[string]any{"current_limit": originalI}
		default:
			continue
		}
		if e := service.Input.Validate(original); e != nil {
			return fmt.Errorf("初始设定超出当前允许恢复范围，未执行控制：%w", e)
		}
	}
	defer func() {
		// 中断只停止测试步骤；恢复使用独立上下文并由执行器限定每次通信时间。
		callContext = context.Background()
		recovery["attempted"] = true
		recovery["original_voltage"] = originalV
		recovery["original_current_limit"] = originalI
		recovery["original_enabled"] = originalOn
		var cleanup error
		for _, step := range []struct {
			key  string
			args map[string]any
		}{{"output.set", map[string]any{"enabled": false}}, {"voltage.set", map[string]any{"voltage": originalV}}, {"current_limit.set", map[string]any{"current_limit": originalI}}} {
			if _, e := call(step.key, step.args); e != nil {
				cleanup = errors.Join(cleanup, e)
				break
			}
		}
		if cleanup == nil {
			_, cleanup = call("output.set", map[string]any{"enabled": originalOn})
		}
		if cleanup == nil {
			finalSettings, err := call("settings.read", map[string]any{})
			cleanup = err
			if cleanup == nil && (finalSettings.Result["voltage"] != originalV || finalSettings.Result["current_limit"] != originalI) {
				cleanup = errors.New("最终设定值与初始值不一致")
			}
		}
		if cleanup == nil {
			finalState, err := call("output.read", map[string]any{})
			cleanup = err
			if cleanup == nil && finalState.Result["enabled"] != originalOn {
				cleanup = errors.New("最终输出状态与初始状态不一致")
			}
		}
		if *device == "itech-it6722" {
			if e := m.FinishItechTest(context.Background(), d.ID); e != nil {
				cleanup = errors.Join(cleanup, e)
			} else {
				recovery["local_control"] = "command_sent"
			}
		}
		recovery["confirmed"] = cleanup == nil
		if cleanup != nil {
			recovery["error"] = cleanup.Error()
			ret = errors.Join(ret, fmt.Errorf("初始状态恢复无法确认：%w", cleanup))
		}
	}()
	steps := []struct {
		key   string
		args  map[string]any
		dwell bool
	}{{"output.set", map[string]any{"enabled": false}, false}, {"voltage.set", map[string]any{"voltage": 5.0}, false}, {"current_limit.set", map[string]any{"current_limit": .5}, false}, {"output.set", map[string]any{"enabled": true}, true}, {"voltage.set", map[string]any{"voltage": 9.0}, true}, {"current_limit.set", map[string]any{"current_limit": 1.0}, true}, {"output.set", map[string]any{"enabled": false}, false}}
	for _, step := range steps {
		if _, e = call(step.key, step.args); e != nil {
			return e
		}
		if step.dwell {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(8 * time.Second):
			}
			if _, e = call("measurement.read", map[string]any{}); e != nil {
				return e
			}
		}
	}
	return nil
}
