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
