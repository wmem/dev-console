package installer

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

func initialPort(selected int, specified, passwordStdin bool) (int, error) {
	if specified {
		return selected, nil
	}
	if passwordStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		return 0, errors.New("首次非交互安装必须使用--port明确指定端口，例如--port 8080")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("无法打开交互终端，请使用--port指定端口：%w", err)
	}
	defer tty.Close()
	return promptPort(tty, tty)
}

func parsePort(text string) (int, error) {
	if text == "" {
		return 0, errors.New("端口必须是1～65535的整数")
	}
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, errors.New("端口必须是1～65535的整数")
		}
	}
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("端口必须是1～65535的整数")
	}
	return port, nil
}

func portAvailable(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("端口必须是1～65535的整数")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("端口%d无法监听，可能已被占用或权限不足，请选择其他端口：%w", port, err)
	}
	return listener.Close()
}

func promptPort(input io.Reader, output io.Writer) (int, error) {
	return readPort(input, output, portAvailable)
}

func readPort(input io.Reader, output io.Writer, probe func(int) error) (int, error) {
	for {
		fmt.Fprint(output, "设置HTTP监听端口 [8080]：")
		// 一字节一字节读取，不预取下一行，避免提前消费随后输入的密码。
		var line []byte
		var one [1]byte
		for {
			if _, err := io.ReadFull(input, one[:]); err != nil {
				return 0, fmt.Errorf("读取端口已中断，未开始安装：%w", err)
			}
			if one[0] == '\n' {
				break
			}
			line = append(line, one[0])
			if len(line) > 128 {
				return 0, errors.New("端口输入过长，未开始安装")
			}
		}
		text := strings.TrimSpace(string(line))
		if text == "" {
			text = "8080"
		}
		port, err := parsePort(text)
		if err == nil {
			err = probe(port)
		}
		if err != nil {
			fmt.Fprintln(output, err)
			continue
		}
		return port, nil
	}
}
