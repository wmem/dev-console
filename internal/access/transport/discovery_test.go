package transport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUSBAndACMDiscoveryKeepsEndpointIdentity(t *testing.T) {
	root := t.TempDir()
	class := filepath.Join(root, "sys", "class", "tty")
	dev := filepath.Join(root, "dev")
	for _, name := range []string{"ttyUSB0", "ttyACM2"} {
		usb := filepath.Join(root, "sys", "devices", name)
		iface := filepath.Join(usb, "interface")
		leaf := filepath.Join(iface, "tty")
		for _, dir := range []string{leaf, filepath.Join(class, name), filepath.Join(dev, "serial", "by-id"), filepath.Join(dev, "serial", "by-path")} {
			if e := os.MkdirAll(dir, 0755); e != nil {
				t.Fatal(e)
			}
		}
		for k, v := range map[string]string{"idVendor": "1a86", "idProduct": "7523", "product": "测试设备", "serial": "USB-" + name} {
			if e := os.WriteFile(filepath.Join(usb, k), []byte(v), 0600); e != nil {
				t.Fatal(e)
			}
		}
		if e := os.WriteFile(filepath.Join(iface, "bInterfaceNumber"), []byte("00"), 0600); e != nil {
			t.Fatal(e)
		}
		node := filepath.Join(dev, name)
		if e := os.WriteFile(node, nil, 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Symlink(leaf, filepath.Join(class, name, "device")); e != nil {
			t.Fatal(e)
		}
		for _, dir := range []string{"by-id", "by-path"} {
			if e := os.Symlink(node, filepath.Join(dev, "serial", dir, name)); e != nil {
				t.Fatal(e)
			}
		}
	}
	ports, e := DiscoverPortsAt(class, dev)
	if e != nil || len(ports) != 2 {
		t.Fatal(ports, e)
	}
	for _, p := range ports {
		if p.USB.VID != "1A86" || p.USB.Serial == "" || p.USB.Interface != "00" || len(p.ByID) != 1 || len(p.ByPath) != 1 {
			t.Fatal(p)
		}
		if BindingMode(p.ByID[0]) != "usb_identity" || BindingMode(p.ByPath[0]) != "usb_port" {
			t.Fatal(p)
		}
	}
}
