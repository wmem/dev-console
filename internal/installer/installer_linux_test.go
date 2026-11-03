package installer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devconsole/internal/buildinfo"
	"devconsole/internal/settings"
	"devconsole/internal/storage"
)

func TestWritableInstallationPathsAccepted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("已有配置"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	eng := &engine{}
	for _, p := range []string{dir, file} {
		if err := eng.checkPath(p); err != nil {
			t.Fatal("不应根据属主或可写权限拒绝已有路径", p, err)
		}
	}
	for _, p := range []string{dir, file} {
		s, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if s.Mode().Perm()&0022 == 0 {
			t.Fatal("检查改动了已有路径权限", p)
		}
	}
}

func TestInstallationLockAndUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	a, e := acquireLock(lock)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := acquireLock(lock); e == nil {
		b.Close()
		t.Fatal("并发安装未拒绝")
	}
	a.Close()
	b, e := acquireLock(lock)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("原内容"), 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if e := copyFile(link, filepath.Join(dir, "copy"), 0600); e == nil {
		t.Fatal("复制跟随了源链接")
	}
	if e := copyFile(target, link, 0600); e == nil {
		t.Fatal("复制覆盖了目标链接")
	}
	info := buildinfo.Info{Version: "0.1.0"}
	eng := &engine{root: link, unitPath: filepath.Join(dir, "unit"), info: info}
	if e := eng.apply(); e == nil {
		t.Fatal("符号链接安装目录未拒绝")
	}
	os.WriteFile(eng.unitPath, []byte("其他服务"), 0600)
	if e := eng.checkUnit(); e == nil {
		t.Fatal("其他systemd文件被接管")
	}
}

// 此测试使用真实程序与SQLite，替代特权账号和服务管理操作，不接触真实串口或系统服务。
func TestInstallLifecycle(t *testing.T) {
	oldBinary := os.Getenv("DCONSOLE_TEST_OLD")
	newBinary := os.Getenv("DCONSOLE_TEST_NEW")
	failedBinary := os.Getenv("DCONSOLE_TEST_FAILED")
	if oldBinary == "" || newBinary == "" || failedBinary == "" {
		t.Skip("由npm run test:install构建三个独立版本后执行")
	}
	base := t.TempDir()
	// 模拟/opt属于部署用户且可被组和其他用户写入，验证实际首装不受此限制。
	if err := os.Chmod(base, 0777); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "dconsole")
	unitPath := filepath.Join(base, serviceName)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	_, portText, _ := net.SplitHostPort(address)
	selectedPort, _ := strconv.Atoi(portText)
	listener.Close()
	random := make([]byte, 24)
	rand.Read(random)
	password := hex.EncodeToString(random)
	var server *exec.Cmd
	var commands []string
	bootstraps := 0
	stop := func() error {
		if server == nil {
			return nil
		}
		server.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- server.Wait() }()
		select {
		case <-done:
			server = nil
			// systemctl stop以进程已结束为成功，不要求服务以0退出。
			return nil
		case <-time.After(5 * time.Second):
			server.Process.Kill()
			<-done
			server = nil
			return errors.New("测试服务停机超时")
		}
	}
	t.Cleanup(func() { stop() })
	ctl := func(args ...string) error {
		commands = append(commands, strings.Join(args, " "))
		switch args[0] {
		case "stop":
			return stop()
		case "start":
			if server != nil {
				return nil
			}
			server = exec.Command(filepath.Join(root, "dconsole"), "serve", "--config", filepath.Join(root, "config.yaml"))
			server.Stdout = io.Discard
			server.Stderr = io.Discard
			return server.Start()
		case "is-active":
			if server == nil {
				return errors.New("服务未运行")
			}
		}
		return nil
	}
	makeEngine := func(binary string) *engine {
		info, e := readInfo(binary)
		if e != nil {
			t.Fatal(e)
		}
		return &engine{root: root, unitPath: unitPath, source: binary, info: info, choosePort: func() (int, error) { return selectedPort, nil }, uid: os.Getuid(), gid: os.Getgid(), prepare: func() error { return nil }, systemctl: ctl, chown: func(string, int, int) error { return nil }, checkHealth: health, output: io.Discard, bootstrap: func(binary, data string) error {
			bootstraps++
			generated, err := settings.Load(filepath.Join(filepath.Dir(binary), "config.yaml"))
			if err != nil {
				return err
			}
			if generated.Listen != fmt.Sprintf("0.0.0.0:%d", selectedPort) {
				return fmt.Errorf("首次配置未保存所选端口：%s", generated.Listen)
			}
			// 仅调整独立测试实例端口，不使用体验服务的8080或任何真实设备。
			cfg := fmt.Sprintf("listen: %q\npublic_base_url: %q\ndata_dir: %q\n", address, "http://"+address, filepath.Join(root, "data"))
			if e := os.WriteFile(filepath.Join(filepath.Dir(binary), "config.yaml"), []byte(cfg), 0640); e != nil {
				return e
			}
			cmd := exec.Command(binary, "admin", "bootstrap", "--data-dir", data, "--username", "admin", "--password-stdin")
			cmd.Stdin = strings.NewReader(password + "\n")
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			return cmd.Run()
		}}
	}
	t.Run("browser_blocked_port_rejected_before_preparation", func(t *testing.T) {
		failed := makeEngine(oldBinary)
		prepared := false
		failed.prepare = func() error { prepared = true; return nil }
		failed.choosePort = func() (int, error) { return 10080, nil }
		if err := failed.apply(); err == nil || !strings.Contains(err.Error(), "浏览器禁止") {
			t.Fatal("浏览器禁用端口未拦截", err)
		}
		if prepared || len(commands) != 0 {
			t.Fatal("浏览器禁用端口仍触发安装准备")
		}
	})
	t.Run("occupied_port_rejected_before_preparation", func(t *testing.T) {
		busy, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		failed := makeEngine(oldBinary)
		prepared := false
		failed.prepare = func() error { prepared = true; return nil }
		failed.choosePort = func() (int, error) { return busy.Addr().(*net.TCPAddr).Port, nil }
		if err = failed.apply(); err == nil || !strings.Contains(err.Error(), "无法监听") {
			t.Fatal("占用端口未拒绝", err)
		}
		if prepared || len(commands) != 0 {
			t.Fatal("端口检查失败后仍准备账号或修改服务")
		}
		if _, err = os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("端口检查失败后创建了安装目录")
		}
	})
	t.Run("bootstrap_failure_leaves_no_installation", func(t *testing.T) {
		failed := makeEngine(oldBinary)
		failed.bootstrap = func(string, string) error { return errors.New("注入初始化失败") }
		if err := failed.apply(); err == nil {
			t.Fatal("初始化失败未上报")
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("初始化失败留下正式安装目录")
		}
		if len(commands) != 0 {
			t.Fatal("初始化失败仍操作了服务")
		}
	})
	first := makeEngine(oldBinary)
	first.serialGroup = "serial-test"
	first.checkHealth = func(cfg settings.Config, info buildinfo.Info) error {
		if err := health(cfg, info); err != nil {
			return err
		}
		return errors.New("注入首次服务检查失败")
	}
	t.Run("first_start_failure_keeps_initialized_data", func(t *testing.T) {
		if err := first.apply(); err == nil {
			t.Fatal("首次服务检查失败未上报")
		}
		if _, err := os.Stat(filepath.Join(root, "data", "platform.db")); err != nil {
			t.Fatal("首次失败删除了已初始化数据", err)
		}
		if bootstraps != 1 || server != nil {
			t.Fatal("初始化次数或失败停机状态错误")
		}
	})
	repairFirst := makeEngine(oldBinary)
	repairFirst.reinstall = true
	if e = repairFirst.apply(); e != nil {
		t.Fatal(e)
	}
	configBefore, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
	unitBefore, _ := os.ReadFile(filepath.Join(root, serviceName))
	if !bytes.Contains(unitBefore, []byte("SupplementaryGroups=serial-test")) {
		t.Fatal("首次串口组未写入服务")
	}
	if target, e := os.Readlink(unitPath); e != nil || target != filepath.Join(root, serviceName) {
		t.Fatal("服务链接不匹配", e)
	}
	for _, item := range []struct {
		name string
		mode os.FileMode
	}{{"dconsole", 0755}, {"config.yaml", 0640}, {"data", 0700}, {"logs", 0700}} {
		s, e := os.Stat(filepath.Join(root, item.name))
		if e != nil || s.Mode().Perm() != item.mode {
			t.Fatal("安装权限错误", item.name, e)
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	var cookie, csrf string
	call := func(t *testing.T, method, route string, body any, token string) map[string]any {
		t.Helper()
		var data io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			data = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "http://"+address+"/api/v1"+route, data)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else if cookie != "" {
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			t.Fatalf("%s %s返回%d", method, route, res.StatusCode)
		}
		if route == "/auth/login" {
			cookie = strings.Split(res.Header.Get("Set-Cookie"), ";")[0]
		}
		var out map[string]any
		if e = json.NewDecoder(res.Body).Decode(&out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	login := call(t, "POST", "/auth/login", map[string]any{"username": "admin", "password": password}, "")
	csrf = login["csrf"].(string)
	project := call(t, "POST", "/admin/projects", map[string]any{"key": "install-test", "display_name": "升级保留验证", "enabled": true}, "")
	projectID := project["id"].(string)
	device := call(t, "POST", "/admin/devices", map[string]any{"template_key": "itech-it6722", "display_name": "离线设备", "connection_config": map[string]any{"path": filepath.Join(base, "absent-device")}}, "")
	call(t, "POST", "/admin/projects/"+projectID+"/services", map[string]any{"key": "power", "display_name": "输出", "enabled": true, "device_id": device["id"], "device_service_key": "output.set"}, "")
	token := call(t, "POST", "/admin/projects/"+projectID+"/tokens", map[string]any{"name": "保留验证"}, "")["secret"].(string)
	verifyBusiness := func(t *testing.T) {
		t.Helper()
		p := call(t, "GET", "/projects/install-test", nil, token)
		if p["id"] != projectID {
			t.Fatal("项目ID或Token丢失")
		}
		call(t, "GET", "/auth/me", nil, "")
		d := call(t, "GET", "/admin/devices/"+device["id"].(string), nil, "")
		if d["device"].(map[string]any)["id"] != device["id"] {
			t.Fatal("设备ID丢失")
		}
		res, e := client.Get("http://" + address + "/healthz")
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		req, _ := http.NewRequest("GET", "http://"+address+"/api/v1/projects/install-test/docs", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		doc, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(doc.Body)
		doc.Body.Close()
		if doc.StatusCode != 200 || !bytes.Contains(body, []byte("/services/power/invoke")) {
			t.Fatal("原Token无法读取原绑定的项目文档")
		}
	}
	t.Run("same_version_noop", func(t *testing.T) {
		before := len(commands)
		if e = makeEngine(oldBinary).apply(); e != nil {
			t.Fatal(e)
		}
		if len(commands) != before || bootstraps != 1 {
			t.Fatal("重复执行触发了服务变更或管理员重建")
		}
	})
	t.Run("existing_port_cannot_be_overridden", func(t *testing.T) {
		changed := makeEngine(newBinary)
		changed.portSpecified = true
		before := len(commands)
		if err := changed.apply(); err == nil || !strings.Contains(err.Error(), "仅用于首次安装") {
			t.Fatal("升级允许用端口参数覆盖原配置", err)
		}
		cfg, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
		if len(commands) != before || !bytes.Equal(cfg, configBefore) {
			t.Fatal("拒绝端口修改时仍改变服务或配置")
		}
	})
	t.Run("blocked_public_url_refused_without_stopping_existing_service", func(t *testing.T) {
		before := len(commands)
		// 保留实际监听端口，只把对外URL改为浏览器禁用端口，核对升级预检。
		broken := fmt.Sprintf("listen: %q\npublic_base_url: \"http://127.0.0.1:10080\"\ndata_dir: %q\n", address, filepath.Join(root, "data"))
		if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(broken), 0640); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(filepath.Join(root, "config.yaml"), configBefore, 0640)
		if err := makeEngine(newBinary).apply(); err == nil || !strings.Contains(err.Error(), "浏览器禁止") {
			t.Fatal("升级预检未拒绝无法浏览的URL", err)
		}
		if len(commands) != before {
			t.Fatal("配置预检失败仍停止了原服务")
		}
	})
	t.Run("upgrade_preserves_state", func(t *testing.T) {
		for _, p := range []string{root, filepath.Join(root, "dconsole")} {
			if err := os.Chmod(p, 0777); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []string{filepath.Join(root, "config.yaml"), filepath.Join(root, serviceName)} {
			if err := os.Chmod(p, 0666); err != nil {
				t.Fatal(err)
			}
		}
		if e = makeEngine(newBinary).apply(); e != nil {
			t.Fatal(e)
		}
		verifyBusiness(t)
		cfg, _ := os.ReadFile(filepath.Join(root, "config.yaml"))
		unit, _ := os.ReadFile(filepath.Join(root, serviceName))
		if !bytes.Equal(cfg, configBefore) || !bytes.Equal(unit, unitBefore) {
			t.Fatal("升级覆盖了配置或服务定义")
		}
		for _, p := range []string{root, filepath.Join(root, "config.yaml"), filepath.Join(root, serviceName)} {
			s, err := os.Stat(p)
			if err != nil || s.Mode().Perm()&0022 == 0 {
				t.Fatal("升级改动了已有目录、配置或服务定义的权限", p, err)
			}
		}
		if bootstraps != 1 {
			t.Fatal("升级重新创建管理员")
		}
	})
	t.Run("refuse_downgrade", func(t *testing.T) {
		before := len(commands)
		if e = makeEngine(oldBinary).apply(); e == nil {
			t.Fatal("降级未拒绝")
		}
		if before != len(commands) {
			t.Fatal("拒绝前操作了服务")
		}
	})
	t.Run("same_version_repair_missing_unit", func(t *testing.T) {
		os.Remove(unitPath)
		repair := makeEngine(newBinary)
		repair.reinstall = true
		if e = repair.apply(); e != nil {
			t.Fatal(e)
		}
		verifyBusiness(t)
		if bootstraps != 1 {
			t.Fatal("修复重建管理员")
		}
	})
	t.Run("health_failure_restores_binary", func(t *testing.T) {
		failed := makeEngine(failedBinary)
		oldHealth := failed.checkHealth
		failed.checkHealth = func(cfg settings.Config, info buildinfo.Info) error {
			if info.Version == failed.info.Version {
				return errors.New("注入新版本健康检查失败")
			}
			return oldHealth(cfg, info)
		}
		if e = failed.apply(); e == nil || !strings.Contains(e.Error(), "已恢复旧版本") {
			t.Fatal(e)
		}
		got, _ := readInfo(filepath.Join(root, "dconsole"))
		expected, _ := readInfo(newBinary)
		if got.Version != expected.Version {
			t.Fatal("旧二进制未恢复")
		}
		verifyBusiness(t)
	})
	t.Run("schema_change_failure_restores_full_data", func(t *testing.T) {
		failed := makeEngine(failedBinary)
		oldHealth := failed.checkHealth
		failed.checkHealth = func(cfg settings.Config, info buildinfo.Info) error {
			if info.Version == failed.info.Version {
				s, e := storage.Open(filepath.Join(root, "data"))
				if e != nil {
					return e
				}
				_, e = s.DB.Exec("INSERT INTO schema_migrations VALUES(?)", storage.CurrentVersion+1)
				s.Close()
				if e != nil {
					return e
				}
				return errors.New("注入迁移后失败")
			}
			return oldHealth(cfg, info)
		}
		if e = failed.apply(); e == nil || !strings.Contains(e.Error(), "已恢复旧版本") {
			t.Fatal(e)
		}
		if v, e := storage.InspectVersion(filepath.Join(root, "data")); e != nil || v != storage.CurrentVersion {
			t.Fatal("停机数据备份未恢复", v, e)
		}
		verifyBusiness(t)
	})
	t.Run("backup_failure_keeps_original", func(t *testing.T) {
		link := filepath.Join(root, "data", "unsafe-link")
		os.Symlink(filepath.Join(root, "config.yaml"), link)
		if e = makeEngine(failedBinary).apply(); e == nil {
			t.Fatal("备份跟随了数据目录链接")
		}
		os.Remove(link)
		verifyBusiness(t)
	})
	t.Run("cannot_stop_failed_version_keeps_backup_without_restore", func(t *testing.T) {
		failed := makeEngine(failedBinary)
		newStarted := false
		failed.checkHealth = func(settings.Config, buildinfo.Info) error {
			newStarted = true
			return errors.New("注入新版本检查失败")
		}
		failed.systemctl = func(args ...string) error {
			if newStarted && args[0] == "stop" {
				return errors.New("注入停止新服务失败")
			}
			return ctl(args...)
		}
		if err := failed.apply(); err == nil || !strings.Contains(err.Error(), "未恢复数据") {
			t.Fatal("无法确认停机时不应自动恢复数据", err)
		}
		got, err := readInfo(filepath.Join(root, "dconsole"))
		if err != nil || got.Version != failed.info.Version {
			t.Fatal("新服务仍可能运行时替换了二进制", err)
		}
	})
}
