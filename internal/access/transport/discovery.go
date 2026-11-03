package transport

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"go.bug.st/serial"
)

// USBIdentity标识USB端点，不代表适配器后面的仪器身份。多接口设备需同时区分接口及端口。
type USBIdentity struct {
	VID       string `json:"vid"`
	PID       string `json:"pid"`
	Serial    string `json:"serial"`
	Interface string `json:"interface"`
	Port      string `json:"port"`
}
type PortInfo struct {
	Path    string
	USB     USBIdentity
	Product string
	ByID    []string
	ByPath  []string
}

var usbTTY = regexp.MustCompile(`^tty(USB|ACM)[0-9]+$`)

func BindingMode(path string) string {
	p := filepath.ToSlash(path)
	if strings.Contains(p, "/serial/by-id/") {
		return "usb_identity"
	}
	if strings.Contains(p, "/serial/by-path/") {
		return "usb_port"
	}
	return "path"
}
func DiscoverPorts() ([]PortInfo, error) {
	if runtime.GOOS == "linux" {
		return DiscoverPortsAt("/sys/class/tty", "/dev")
	}
	paths, e := serial.GetPortsList()
	if e != nil {
		return nil, e
	}
	out := []PortInfo{}
	for _, p := range paths {
		out = append(out, PortInfo{Path: p})
	}
	return out, nil
}

// DiscoverPortsAt只读取内核元数据和udev链接，不打开串口、不发送探测报文。参数也供独立sysfs夹具验证。
func DiscoverPortsAt(classDir, devDir string) ([]PortInfo, error) {
	entries, e := os.ReadDir(classDir)
	if e != nil {
		return nil, e
	}
	out := []PortInfo{}
	for _, entry := range entries {
		if !usbTTY.MatchString(entry.Name()) {
			continue
		}
		node := filepath.Join(devDir, entry.Name())
		if _, e = os.Stat(node); e != nil {
			continue
		}
		p := PortInfo{Path: EndpointPath(node)}
		ancestor, err := filepath.EvalSymlinks(filepath.Join(classDir, entry.Name(), "device"))
		if err != nil {
			continue
		}
		for current := ancestor; current != "." && current != string(filepath.Separator); current = filepath.Dir(current) {
			if p.USB.Interface == "" {
				p.USB.Interface = readAttribute(current, "bInterfaceNumber")
			}
			if p.USB.Port == "" {
				p.USB.Port = readAttribute(current, "port_number")
			}
			if vid := readAttribute(current, "idVendor"); vid != "" {
				p.USB.VID = strings.ToUpper(vid)
				p.USB.PID = strings.ToUpper(readAttribute(current, "idProduct"))
				p.USB.Serial = readAttribute(current, "serial")
				p.Product = readAttribute(current, "product")
				break
			}
		}
		p.ByID = aliases(filepath.Join(devDir, "serial", "by-id"), p.Path)
		p.ByPath = aliases(filepath.Join(devDir, "serial", "by-path"), p.Path)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func readAttribute(dir, name string) string {
	b, e := os.ReadFile(filepath.Join(dir, name))
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func aliases(dir, target string) []string {
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil
	}
	out := []string{}
	for _, entry := range entries {
		p := filepath.Join(dir, entry.Name())
		resolved, e := filepath.EvalSymlinks(p)
		if e == nil && filepath.Clean(resolved) == target {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
