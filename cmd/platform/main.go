package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/auth"
	"devconsole/internal/buildinfo"
	"devconsole/internal/core"
	"devconsole/internal/httpapi"
	"devconsole/internal/installer"
	"devconsole/internal/settings"
	"devconsole/internal/storage"
	"devconsole/web"
	"golang.org/x/term"
)

type config = settings.Config

func main() {
	if e := run(); e != nil {
		slog.Error("执行失败", "原因", e.Error())
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("用法：dconsole version | install | admin bootstrap | serve | hardware-test")
	}
	switch os.Args[1] {
	case "version", "--version":
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	case "install":
		return installer.Run(os.Args[2:])
	case "admin":
		return bootstrap(os.Args[2:])
	case "serve":
		return serve(os.Args[2:])
	case "hardware-test":
		return hardwareTest(os.Args[2:])
	default:
		return errors.New("未知子命令")
	}
}
func bootstrap(args []string) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return errors.New("用法：platform admin bootstrap --data-dir ./data --username admin")
	}
	f := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	dir := f.String("data-dir", "./data", "数据目录")
	name := f.String("username", "admin", "管理员用户名")
	stdin := f.Bool("password-stdin", false, "显式从标准输入读取密码（自动化环境）")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	var password []byte
	if *stdin {
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return errors.New("标准输入没有密码")
		}
		password = append([]byte{}, sc.Bytes()...)
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("需要交互终端，自动化请显式使用--password-stdin")
		}
		fmt.Fprint(os.Stderr, "设置管理员密码（至少10个字符）：")
		p, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil {
			return e
		}
		fmt.Fprint(os.Stderr, "再次输入密码：")
		confirm, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil {
			return e
		}
		if !bytes.Equal(p, confirm) {
			return errors.New("两次密码不一致")
		}
		password = p
	}
	s, e := storage.Open(*dir)
	if e != nil {
		return e
	}
	defer s.Close()
	_, e = auth.New(s).Bootstrap(*name, string(password))
	clear(password)
	if e == nil {
		fmt.Println("管理员已创建；请启动服务并登录")
	}
	return e
}
func loadConfig(file string) (config, error) { return settings.Load(file) }
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	file := f.String("config", "", "YAML配置文件")
	if e := f.Parse(args); e != nil {
		return e
	}
	cfg, e := loadConfig(*file)
	if e != nil {
		return e
	}
	s, e := storage.Open(cfg.DataDir)
	if e != nil {
		return e
	}
	defer s.Close()
	k := core.New(s)
	if e = k.Recover(); e != nil {
		return e
	}
	m := manager.New(s, k)
	k.Provisioner = m
	k.Invoker = m
	defer m.Close()
	if e = m.Restore(context.Background()); e != nil {
		return e
	}
	devices, e := k.Devices()
	if e != nil {
		return e
	}
	for _, d := range devices {
		if e = m.Test(context.Background(), d.ID); e != nil {
			slog.Warn("设备连接探测未通过", "设备", d.DisplayName, "原因", e.Error())
		}
	}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: httpapi.New(k, auth.New(s), cfg.PublicBaseURL, web.Assets()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		slog.Info("平台启动", "版本", buildinfo.Version, "提交", buildinfo.Commit, "监听", cfg.Listen, "访问地址", cfg.PublicBaseURL, "数据目录", filepath.Clean(cfg.DataDir))
		done <- httpServer.ListenAndServe()
	}()
	select {
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}
