package manager

import (
	"context"
	"fmt"

	"devconsole/internal/access/transport"
	c "devconsole/internal/contracts"
)

func (m *Manager) catalog() ([]transport.PortInfo, error) {
	if m.Ports != nil {
		return m.Ports()
	}
	return transport.DiscoverPorts()
}
func identityCount(ports []transport.PortInfo, id transport.USBIdentity) int {
	n := 0
	for _, p := range ports {
		if p.USB == id {
			n++
		}
	}
	return n
}
func (m *Manager) DiscoverConnections(ctx context.Context, key string) ([]c.ConnectionChoice, error) {
	found := false
	for _, t := range m.ListTemplates(ctx) {
		if t.Key == key {
			found = true
		}
	}
	if !found {
		return nil, c.Fail("INVALID_ARGUMENT", "未知接入模板")
	}
	ports, e := m.catalog()
	if e != nil {
		return nil, fmt.Errorf("无法读取本机串口信息：%w", e)
	}
	out := []c.ConnectionChoice{}
	for _, p := range ports {
		label := p.Product
		if label == "" {
			label = "串口"
		}
		for _, path := range p.ByID {
			ok := p.USB.Serial != "" && identityCount(ports, p.USB) == 1
			warning := ""
			if p.USB.Serial == "" {
				warning = "没有唯一USB序列号，该by-id名称不能区分相同适配器，请选择固定插口"
			} else if !ok {
				warning = "USB身份存在多个候选，请选择固定插口或排查重复序列号"
			}
			out = append(out, c.ConnectionChoice{Value: path, Label: label + " · USB序列号 " + p.USB.Serial + " · " + p.Path, Mode: "usb_identity", ResolvedPath: p.Path, Selectable: ok, Warning: warning})
		}
		for _, path := range p.ByPath {
			out = append(out, c.ConnectionChoice{Value: path, Label: label + " · 固定USB插口 · " + p.Path, Mode: "usb_port", ResolvedPath: p.Path, Selectable: true, Warning: "跟随固定插口和Hub拓扑；换插口时编辑本设备重新选择，不能据此证明仪器身份"})
		}
		out = append(out, c.ConnectionChoice{Value: p.Path, Label: label + " · 当前编号 · " + p.Path, Mode: "path", ResolvedPath: p.Path, Selectable: true, Warning: "编号可能随插拔变化；适用于手动指定，优先选择稳定绑定方式"})
	}
	return out, nil
}

// bindingIdentity在真正发送业务报文前校验USB端点；离线配置允许保存，已记忆的身份不可静默放弃。
func (m *Manager) bindingIdentity(cfg transport.Config, expected *transport.USBIdentity, allowMissing ...bool) (*transport.USBIdentity, error) {
	if transport.BindingMode(cfg.Path) != "usb_identity" && expected == nil {
		return nil, nil
	}
	ports, e := m.catalog()
	if e != nil {
		return nil, c.Fail("DEVICE_OFFLINE", "无法核验USB身份："+e.Error())
	}
	endpoint := transport.EndpointPath(cfg.Path)
	for _, p := range ports {
		if p.Path != endpoint {
			continue
		}
		if p.USB.Serial == "" {
			return nil, c.Fail("CONFIG_CONFLICT", "该by-id标识没有唯一USB序列号，请使用固定插口by-path")
		}
		if identityCount(ports, p.USB) != 1 {
			return nil, c.Fail("CONFIG_CONFLICT", "USB身份存在多个候选，拒绝自动选择")
		}
		if expected != nil && *expected != p.USB {
			return nil, c.Fail("CONFIG_CONFLICT", "USB端点身份与保存的绑定不一致，请编辑设备明确重新选择串口")
		}
		identity := p.USB
		return &identity, nil
	}
	if expected != nil {
		if len(allowMissing) > 0 && allowMissing[0] {
			return expected, nil
		}
		return nil, c.Fail("DEVICE_OFFLINE", "保存的USB身份当前不可核验，未尝试其他设备")
	}
	if len(allowMissing) == 0 || !allowMissing[0] {
		return nil, c.Fail("DEVICE_OFFLINE", "该by-id端点当前缺少可核验的USB身份，未尝试连接")
	}
	return nil, nil
}
