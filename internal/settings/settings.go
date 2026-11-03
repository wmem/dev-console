package settings

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"devconsole/internal/webport"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen        string `yaml:"listen"`
	PublicBaseURL string `yaml:"public_base_url"`
	DataDir       string `yaml:"data_dir"`
}

// Load只读取及校验配置，不创建数据库或改变设备状态，供服务和安装预检共用。
func Load(file string) (Config, error) {
	cfg := Config{Listen: "0.0.0.0:8080", DataDir: "./data"}
	if file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return cfg, e
		}
		d := yaml.NewDecoder(bytes.NewReader(b))
		d.KnownFields(true)
		if e = d.Decode(&cfg); e != nil {
			return cfg, e
		}
	}
	host, port, e := net.SplitHostPort(cfg.Listen)
	if e != nil {
		return cfg, errors.New("listen需为IP:端口")
	}
	if cfg.PublicBaseURL == "" {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
			as, _ := net.InterfaceAddrs()
			for _, a := range as {
				ip, _, e := net.ParseCIDR(a.String())
				if e == nil && ip.To4() != nil && !ip.IsLoopback() {
					host = ip.String()
					break
				}
			}
		}
		cfg.PublicBaseURL = "http://" + net.JoinHostPort(host, port)
	}
	u, e := url.Parse(cfg.PublicBaseURL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return cfg, errors.New("public_base_url需为不含凭据、路径、查询参数的HTTP(S)地址")
	}
	if u.Hostname() == "0.0.0.0" || u.Hostname() == "::" {
		return cfg, errors.New("public_base_url不能是监听通配地址")
	}
	if u.Port() != "" {
		externalPort, err := strconv.Atoi(u.Port())
		if err != nil {
			return cfg, errors.New("public_base_url端口无效")
		}
		if err = webport.Validate(externalPort); err != nil {
			return cfg, fmt.Errorf("public_base_url不可用于网页访问：%w", err)
		}
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	if cfg.DataDir == "" {
		return cfg, errors.New("data_dir不能为空")
	}
	return cfg, nil
}
