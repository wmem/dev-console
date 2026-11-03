package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLANConfigAndPublicURL(t *testing.T) {
	cfg, e := loadConfig("")
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Listen != "0.0.0.0:8080" || strings.Contains(cfg.PublicBaseURL, "0.0.0.0") {
		t.Fatal(cfg)
	}
	for _, url := range []string{"http://0.0.0.0:8080", "http://user:password@host", "file:///tmp/x", "http://host/path", "http://host?x=y"} {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if e = os.WriteFile(file, []byte("public_base_url: \""+url+"\"\n"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = loadConfig(file); e == nil {
			t.Fatal("不可信公共地址未拒绝", url)
		}
	}
}

func TestPublicBrowserPortAndReverseProxy(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"generated_blocked", "listen: \"0.0.0.0:10080\"\n", false},
		{"explicit_blocked", "public_base_url: \"http://example.test:10080\"\n", false},
		{"invalid_public_port", "public_base_url: \"http://example.test:65536\"\n", false},
		{"allowed_port", "listen: \"0.0.0.0:10081\"\n", true},
		{"proxy_internal_port", "listen: \"127.0.0.1:10080\"\npublic_base_url: \"https://example.test\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(file); (err == nil) != tc.valid {
				t.Fatal("外部浏览器端口与代理配置判断错误", err)
			}
		})
	}
}
