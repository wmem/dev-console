package main

import (
	"strings"
	"testing"
)

func TestHardwareModeRequiresExplicitSupportedSelection(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		message string
	}{{[]string{"--device", "unknown", "--run"}, "未知设备"}, {[]string{"--device", "longwei-lw3010e", "--address", "0", "--run"}, "从机地址"}, {[]string{"--device", "longwei-lw3010e"}, "--run"}} {
		if e := hardwareTest(tc.args); e == nil || !strings.Contains(e.Error(), tc.message) {
			t.Fatalf("错误选项未在接触设备前拒绝：%v, %v", tc.args, e)
		}
	}
}
