//go:build ignore

// 隔离浏览器验收入口：真实核心、HTTP与嵌入页面，米家请求全部经本地模拟器。
// 不构建进生产程序，不读取真实账号或设备配置。
package main

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devconsole/internal/access/manager"
	"devconsole/internal/access/router"
	"devconsole/internal/access/xiaomi"
	"devconsole/internal/auth"
	"devconsole/internal/core"
	"devconsole/internal/httpapi"
	"devconsole/internal/storage"
	"devconsole/internal/testxiaomi"
	"devconsole/web"
)

func main() {
	dir := flag.String("data-dir", "", "隔离数据目录")
	listen := flag.String("listen", "127.0.0.1:18482", "本地测试监听")
	flag.Parse()
	if *dir == "" {
		panic("需要隔离数据目录")
	}
	s, err := storage.Open(*dir)
	if err != nil {
		panic(err)
	}
	defer s.Close()
	k := core.New(s)
	if err = k.Recover(); err != nil {
		panic(err)
	}
	cloud := testxiaomi.New()
	defer cloud.Close()
	protocol := xiaomi.NewClient()
	protocol.HTTP = cloud.HTTPClient()
	serial := manager.New(s, k)
	defer serial.Close()
	m := xiaomi.New(s, k, protocol)
	defer m.Close()
	access := router.New(s, router.Backend{Route: "builtin-access-1", Provisioner: serial, Invoker: serial}, router.Backend{Route: xiaomi.Route, Provisioner: m, Invoker: m})
	k.Invoker, k.Provisioner = access, access
	if err := m.Restore(context.Background()); err != nil {
		panic(err)
	}
	app := httpapi.New(k, auth.New(s), "http://"+*listen, web.Assets(), httpapi.WithXiaomi(m))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture/confirm", func(w http.ResponseWriter, r *http.Request) { cloud.Confirm(testxiaomi.UserID); w.WriteHeader(204) })
	mux.HandleFunc("POST /fixture/upgrade-spec", func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile("internal/access/xiaomi/testdata/oit.plug.cb7.spec.json")
		if err != nil {
			panic(err)
		}
		raw = bytes.ReplaceAll(raw, []byte("urn:miot-spec-v2:device:outlet:0000A002:oit-cb7:1:0000D030"), []byte(testxiaomi.SpecURN))
		cloud.SetSpec(testxiaomi.SpecURN, raw)
		w.WriteHeader(204)
	})
	mux.Handle("/", app)
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
}
