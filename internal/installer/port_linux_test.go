package installer

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
)

func TestPortParsing(t *testing.T) {
	for _, text := range []string{"", "0", "65536", "-1", "+80", "80x", "8.1", " 80", "999999999999999999999999"} {
		if _, err := parsePort(text); err == nil {
			t.Fatalf("非法端口被接受：%q", text)
		}
	}
	for _, text := range []string{"1", "8080", "65535"} {
		if _, err := parsePort(text); err != nil {
			t.Fatal(text, err)
		}
	}
}

func TestPortPromptPreservesNextInput(t *testing.T) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	busy := listener.Addr().(*net.TCPAddr).Port
	free, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	selected := free.Addr().(*net.TCPAddr).Port
	free.Close()
	input := strings.NewReader(fmt.Sprintf("wrong\n65536\n%d\n%d\nnext-secret-line\n", busy, selected))
	var output bytes.Buffer
	port, err := promptPort(input, &output)
	if err != nil || port != selected {
		t.Fatal(port, err)
	}
	rest, _ := io.ReadAll(input)
	if string(rest) != "next-secret-line\n" {
		t.Fatal("端口提示预取了下一行输入")
	}
	if !strings.Contains(output.String(), "无法监听") {
		t.Fatal("占用端口未提示")
	}
	port, err = readPort(strings.NewReader("\n"), io.Discard, func(p int) error {
		if p != 8080 {
			t.Fatal("默认端口错误", p)
		}
		return nil
	})
	if err != nil || port != 8080 {
		t.Fatal(port, err)
	}
	if _, err = promptPort(strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("输入中断被当成默认端口")
	}
	if _, err = promptPort(strings.NewReader(strings.Repeat("9", 129)+"\n"), io.Discard); err == nil {
		t.Fatal("过长输入未拒绝")
	}
	if port, err = initialPort(selected, true, true); err != nil || port != selected {
		t.Fatal(port, err)
	}
	if _, err = initialPort(0, false, true); err == nil {
		t.Fatal("密码标准输入模式缺省端口未拒绝")
	}
}

func TestPortServicePrivileges(t *testing.T) {
	for _, port := range []int{80, 443, 1023, 1024, 8080} {
		e := &engine{root: installRoot, port: port}
		unit := e.serviceDefinition()
		for _, line := range []string{"AmbientCapabilities=CAP_NET_BIND_SERVICE\n", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE\n"} {
			if strings.Contains(unit, line) != (port < 1024) {
				t.Fatal("低端口权限边界错误", port)
			}
		}
		if !strings.Contains(unit, "User=dconsole\n") || !strings.Contains(unit, "NoNewPrivileges=true\n") {
			t.Fatal("端口选择改变了普通用户或权限限制", port)
		}
	}
}
