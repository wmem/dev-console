package power

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"devconsole/internal/access/transport"
	c "devconsole/internal/contracts"
)

func Services(maxV, maxI float64) []c.Service {
	boolean := c.Schema{Type: "boolean"}
	number := c.Schema{Type: "number"}
	str := c.Schema{Type: "string"}
	out := []c.Service{
		{Key: "output.set", Contract: "switch.set", Title: "设置输出开关", Kind: "operation", Input: c.Object(map[string]c.Schema{"enabled": boolean}, "enabled"), Output: c.Object(map[string]c.Schema{"requested_enabled": boolean, "observed_enabled": boolean}, "requested_enabled", "observed_enabled")},
		{Key: "voltage.set", Contract: "voltage.set", Title: "设置电压", Kind: "operation", Input: c.Object(map[string]c.Schema{"voltage": c.Num(0, maxV, .01, "V")}, "voltage"), Output: c.Object(map[string]c.Schema{"requested_voltage": number, "observed_voltage": number}, "requested_voltage", "observed_voltage")},
		{Key: "current_limit.set", Contract: "current_limit.set", Title: "设置电流限值", Kind: "operation", Input: c.Object(map[string]c.Schema{"current_limit": c.Num(0, maxI, .01, "A")}, "current_limit"), Output: c.Object(map[string]c.Schema{"requested_current_limit": number, "observed_current_limit": number}, "requested_current_limit", "observed_current_limit")},
		{Key: "settings.read", Contract: "power.settings.read", Title: "读取设定值", Kind: "read", Input: c.Object(map[string]c.Schema{}), Output: c.Object(map[string]c.Schema{"voltage": number, "current_limit": number}, "voltage", "current_limit")},
		{Key: "measurement.read", Contract: "power.measurement.read", Title: "读取实测值", Kind: "read", Input: c.Object(map[string]c.Schema{}), Output: c.Object(map[string]c.Schema{"voltage": number, "current": number}, "voltage", "current")},
		{Key: "output.read", Contract: "switch.read", Title: "读取输出状态", Kind: "read", Input: c.Object(map[string]c.Schema{}), Output: c.Object(map[string]c.Schema{"enabled": boolean, "mode": str}, "enabled")},
	}
	for i := range out {
		out[i].ContractVersion = 1
		out[i].TimeoutMS = 5000
		out[i].SupportsVerification = out[i].Kind == "operation"
		out[i].Description = out[i].Title
	}
	return out
}
func CRC(p []byte) uint16 {
	v := uint16(0xffff)
	for _, x := range p {
		v ^= uint16(x)
		for range 8 {
			if v&1 != 0 {
				v = (v >> 1) ^ 0xa001
			} else {
				v >>= 1
			}
		}
	}
	return v
}
func Frame(addr byte, fn byte, reg, value uint16) []byte {
	p := []byte{addr, fn, byte(reg >> 8), byte(reg), byte(value >> 8), byte(value)}
	v := CRC(p)
	return append(p, byte(v), byte(v>>8))
}
func Modbus(w *transport.Wire, addr byte, fn byte, reg, value uint16) ([]byte, error) {
	req := Frame(addr, fn, reg, value)
	if e := w.Write(req, fn == 6); e != nil {
		return nil, e
	}
	head, e := w.Exact(3)
	if e != nil {
		return nil, e
	}
	n := 0
	if head[1]&0x80 != 0 {
		n = 2
	} else if head[1] == 3 {
		if head[2] > 128 {
			return nil, c.Fail("PROTOCOL_ERROR", "Modbus回复长度非法")
		}
		n = int(head[2]) + 2
	} else if head[1] == 6 {
		n = 5
	} else {
		return nil, c.Fail("PROTOCOL_ERROR", "Modbus功能码错误")
	}
	tail, e := w.Exact(n)
	if e != nil {
		return nil, e
	}
	p := append(head, tail...)
	if p[0] != addr || CRC(p[:len(p)-2]) != binary.LittleEndian.Uint16(p[len(p)-2:]) {
		return nil, c.Fail("PROTOCOL_ERROR", "Modbus地址或CRC错误")
	}
	if p[1] == fn|0x80 {
		return nil, c.Fail("DEVICE_ERROR", fmt.Sprintf("Modbus异常码：%d", p[2]))
	}
	if p[1] != fn {
		return nil, c.Fail("PROTOCOL_ERROR", "Modbus功能码不匹配")
	}
	if fn == 6 {
		if string(req) != string(p) {
			return nil, c.Fail("PROTOCOL_ERROR", "Modbus写回显不一致")
		}
	} else if p[2] != byte(value*2) {
		return nil, c.Fail("PROTOCOL_ERROR", "Modbus字节数错误")
	}
	return p, nil
}
func Longwei(w *transport.Wire, cfg transport.Config, key string, p map[string]any) (c.Result, error) {
	result := map[string]any{}
	if key == "output.set" || key == "voltage.set" || key == "current_limit.set" {
		reg := uint16(0x1000)
		value := uint16(0)
		switch key {
		case "output.set":
			reg = 0x1006
			if p["enabled"].(bool) {
				value = 1
			}
		case "voltage.set":
			value = uint16(math.Round(p["voltage"].(float64) * 100))
		case "current_limit.set":
			reg = 0x1001
			value = uint16(math.Round(p["current_limit"].(float64) * 100))
		}
		if _, e := Modbus(w, byte(cfg.Address), 6, reg, value); e != nil {
			return c.Result{}, e
		}
	}
	rx, e := Modbus(w, byte(cfg.Address), 3, 0x1000, 6)
	if e != nil {
		return c.Result{}, e
	}
	a := make([]uint16, 6)
	for i := range a {
		a[i] = binary.BigEndian.Uint16(rx[3+i*2:])
	}
	v, i := float64(a[0])/100, float64(a[1])/100
	enabled := byte(a[4]) == 1
	if byte(a[4]) > 1 {
		return c.Result{}, c.Fail("PROTOCOL_ERROR", "输出状态非法")
	}
	confirm := "read"
	switch key {
	case "output.set":
		result = map[string]any{"requested_enabled": p["enabled"], "observed_enabled": enabled}
		if enabled != p["enabled"] {
			return c.Result{}, c.Fail("VERIFICATION_FAILED", "输出状态回读不一致")
		}
		confirm = "verified"
	case "voltage.set":
		result = map[string]any{"requested_voltage": p["voltage"], "observed_voltage": v}
		if math.Abs(v-p["voltage"].(float64)) > 1e-7 {
			return c.Result{}, c.Fail("VERIFICATION_FAILED", "设定电压回读不一致")
		}
		confirm = "verified"
	case "current_limit.set":
		result = map[string]any{"requested_current_limit": p["current_limit"], "observed_current_limit": i}
		if math.Abs(i-p["current_limit"].(float64)) > 1e-7 {
			return c.Result{}, c.Fail("VERIFICATION_FAILED", "电流限值回读不一致")
		}
		confirm = "verified"
	case "settings.read":
		result = map[string]any{"voltage": v, "current_limit": i}
	case "measurement.read":
		result = map[string]any{"voltage": float64(a[2]) / 100, "current": float64(a[3]) / 100}
	case "output.read":
		result = map[string]any{"enabled": enabled}
		m := map[byte]string{0: "CC", 1: "CV", 2: "OC"}
		if mode, ok := m[byte(a[5])]; ok {
			result["mode"] = mode
		}
	default:
		return c.Result{}, c.Fail("BINDING_INVALID", "未知设备服务")
	}
	return c.Result{Output: result, Confirmation: confirm, ObservedAt: time.Now().UTC(), Source: "live"}, nil
}
func Query(w *transport.Wire, cmd string) (string, error) {
	if e := w.Write([]byte(cmd+"\n"), false); e != nil {
		return "", e
	}
	return w.Line()
}
func Identity(w *transport.Wire) error {
	_, e := ReadIdentity(w)
	return e
}
func ReadIdentity(w *transport.Wire) (string, error) {
	s, e := Query(w, "*IDN?")
	if e != nil {
		return "", e
	}
	parts := strings.Split(s, ",")
	if len(parts) < 3 || strings.TrimSpace(parts[1]) != "IT6722" || strings.TrimSpace(parts[2]) == "" {
		return "", c.Fail("DEVICE_ERROR", "设备型号或序列号不符合IT6722："+s)
	}
	return "IT6722:" + strings.TrimSpace(parts[2]), nil
}
func number(w *transport.Wire, cmd string) (float64, error) {
	s, e := Query(w, cmd)
	if e != nil {
		return 0, e
	}
	v, e := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, c.Fail("PROTOCOL_ERROR", "设备数值回复非法")
	}
	return v, nil
}
func Itech(w *transport.Wire, cfg transport.Config, key string, p map[string]any) (c.Result, error) {
	if e := Identity(w); e != nil {
		return c.Result{}, e
	}
	out := map[string]any{}
	confirm := "read"
	switch key {
	case "voltage.set", "current_limit.set", "output.set":
		if e := w.Write([]byte("SYST:REM\n"), false); e != nil {
			return c.Result{}, e
		}
		cmd, param := "VOLT", "voltage"
		if key == "current_limit.set" {
			cmd, param = "CURR", "current_limit"
		}
		value := 0.0
		if key == "output.set" {
			cmd = "OUTP"
			param = "enabled"
			if p[param].(bool) {
				value = 1
			}
		} else {
			value = p[param].(float64)
		}
		if e := w.Write([]byte(cmd+" "+strconv.FormatFloat(value, 'f', -1, 64)+"\n"), true); e != nil {
			return c.Result{}, e
		}
		v, e := number(w, cmd+"?")
		if e != nil {
			return c.Result{}, e
		}
		if math.Abs(v-value) > 1e-7 {
			return c.Result{}, c.Fail("VERIFICATION_FAILED", "设定值回读不一致")
		}
		if key == "output.set" {
			out["requested_enabled"] = p[param]
			out["observed_enabled"] = v == 1
		} else {
			out["requested_"+param] = p[param]
			out["observed_"+param] = v
		}
		errline, e := Query(w, "SYST:ERR?")
		if e != nil {
			return c.Result{}, e
		}
		parts := strings.SplitN(errline, ",", 2)
		if len(parts) != 2 {
			return c.Result{}, c.Fail("PROTOCOL_ERROR", "错误队列回复非法")
		}
		code, e := strconv.Atoi(strings.TrimSpace(parts[0]))
		if e != nil {
			return c.Result{}, c.Fail("PROTOCOL_ERROR", "错误队列代码非法")
		}
		if code != 0 {
			return c.Result{}, c.Fail("DEVICE_ERROR", "设备错误："+errline)
		}
		confirm = "verified"
	case "settings.read":
		v, e := number(w, "VOLT?")
		if e != nil {
			return c.Result{}, e
		}
		i, e := number(w, "CURR?")
		if e != nil {
			return c.Result{}, e
		}
		out = map[string]any{"voltage": v, "current_limit": i}
	case "measurement.read":
		v, e := number(w, "MEAS:VOLT?")
		if e != nil {
			return c.Result{}, e
		}
		i, e := number(w, "MEAS:CURR?")
		if e != nil {
			return c.Result{}, e
		}
		out = map[string]any{"voltage": v, "current": i}
	case "output.read":
		v, e := number(w, "OUTP?")
		if e != nil {
			return c.Result{}, e
		}
		if v != 0 && v != 1 {
			return c.Result{}, c.Fail("PROTOCOL_ERROR", "输出状态非法")
		}
		out = map[string]any{"enabled": v == 1}
	default:
		return c.Result{}, c.Fail("BINDING_INVALID", "未知设备服务")
	}
	return c.Result{Output: out, Confirmation: confirm, ObservedAt: time.Now().UTC(), Source: "live"}, nil
}
