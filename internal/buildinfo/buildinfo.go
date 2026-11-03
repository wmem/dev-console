// Package buildinfo提供程序内嵌版本，不依赖安装目录中的可变版本文件。
package buildinfo

import (
	"devconsole/internal/storage"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// 以下值由构建脚本通过ldflags注入，直接go build得到的dev版本不能用于安装。
var Version = "dev"
var Commit = "unknown"
var Dirty = "true"

type Info struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Dirty       bool   `json:"dirty"`
	Target      string `json:"target"`
	DatabaseMin int    `json:"database_min"`
	DatabaseMax int    `json:"database_max"`
}

func Current() Info {
	return Info{Version, Commit, Dirty == "true", runtime.GOOS + "/" + runtime.GOARCH, storage.MinVersion, storage.CurrentVersion}
}

func Parse(v string) ([3]uint64, error) {
	var out [3]uint64
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("版本需为不带v前缀的主版本.次版本.修订版本：%q", v)
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return out, fmt.Errorf("版本格式非法：%q", v)
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return out, fmt.Errorf("版本格式非法：%q", v)
			}
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return out, fmt.Errorf("版本数字过大：%q", v)
		}
		out[i] = n
	}
	return out, nil
}

func Compare(a, b string) (int, error) {
	x, e := Parse(a)
	if e != nil {
		return 0, e
	}
	y, e := Parse(b)
	if e != nil {
		return 0, e
	}
	for i := range x {
		if x[i] < y[i] {
			return -1, nil
		}
		if x[i] > y[i] {
			return 1, nil
		}
	}
	return 0, nil
}
