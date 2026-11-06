package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"devconsole/internal/buildinfo"
	"devconsole/internal/secretbox"
	"devconsole/internal/settings"
	"devconsole/internal/storage"
)

// 使用已归档格式1程序验证真正的跨格式安装回退，不把当前代码改版本号当成旧实现。
func TestLegacyInstallerTokenUpgradeRollback(t *testing.T) {
	legacy, current := os.Getenv("DCONSOLE_TEST_LEGACY"), os.Getenv("DCONSOLE_TEST_CURRENT")
	if legacy == "" || current == "" {
		t.Skip("由npm run test:token-upgrade提供旧版安装包与当前程序")
	}
	oldInfo, err := readInfo(legacy)
	if err != nil || oldInfo.DatabaseMax != 1 {
		t.Fatal("需要真实格式1程序", err)
	}
	newInfo, err := readInfo(current)
	if err != nil || newInfo.DatabaseMax != storage.CurrentVersion {
		t.Fatal("需要当前数据库格式的程序", err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "dconsole")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	password := fmt.Sprintf("fixture-password-%d", time.Now().UnixNano())
	var process *exec.Cmd
	stop := func() error {
		if process == nil {
			return nil
		}
		process.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		select {
		case <-done:
			process = nil
			return nil
		case <-time.After(5 * time.Second):
			process.Process.Kill()
			<-done
			process = nil
			return errors.New("测试进程未正常停机")
		}
	}
	t.Cleanup(func() { stop() })
	ctl := func(args ...string) error {
		switch args[0] {
		case "stop":
			return stop()
		case "start":
			if process != nil {
				return nil
			}
			process = exec.Command(filepath.Join(root, "dconsole"), "serve", "--config", filepath.Join(root, "config.yaml"))
			process.Stdout = io.Discard
			process.Stderr = io.Discard
			return process.Start()
		case "is-active":
			if process == nil {
				return errors.New("测试服务未运行")
			}
		}
		return nil
	}
	e := &engine{root: root, unitPath: filepath.Join(base, serviceName), source: legacy, info: oldInfo, choosePort: func() (int, error) { return port, nil }, uid: os.Getuid(), gid: os.Getgid(), prepare: func() error { return nil }, systemctl: ctl, chown: func(string, int, int) error { return nil }, checkHealth: health, output: io.Discard}
	e.bootstrap = func(binary, data string) error {
		config := fmt.Sprintf("listen: %q\npublic_base_url: %q\ndata_dir: %q\n", address, "http://"+address, filepath.Join(root, "data"))
		if err := os.WriteFile(filepath.Join(filepath.Dir(binary), "config.yaml"), []byte(config), 0640); err != nil {
			return err
		}
		cmd := exec.Command(binary, "admin", "bootstrap", "--data-dir", data, "--password-stdin")
		cmd.Stdin = strings.NewReader(password + "\n")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	if err = e.apply(); err != nil {
		t.Fatal("旧版初装失败", err)
	}
	var cookie, csrf string
	client := &http.Client{Timeout: 3 * time.Second}
	call := func(method, route string, body any, bearer string, want int) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "http://"+address+"/api/v1"+route, reader)
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else if cookie != "" {
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s返回%d，预期%d", method, route, res.StatusCode, want)
		}
		if route == "/auth/login" {
			cookie = strings.Split(res.Header.Get("Set-Cookie"), ";")[0]
		}
		out := map[string]any{}
		if strings.Contains(res.Header.Get("Content-Type"), "application/json") {
			if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatal("测试响应解析失败")
			}
		} else {
			io.Copy(io.Discard, res.Body)
		}
		return out
	}
	login := call("POST", "/auth/login", map[string]any{"username": "admin", "password": password}, "", 200)
	csrf = login["csrf"].(string)
	project := call("POST", "/admin/projects", map[string]any{"key": "legacy-live", "display_name": "旧版迁移", "enabled": true}, "", 201)
	pid := project["id"].(string)
	issued := call("POST", "/admin/projects/"+pid+"/tokens", map[string]any{"name": "真实旧Token"}, "", 201)
	oldRaw := issued["secret"].(string)
	oldID := issued["token"].(map[string]any)["id"].(string)
	e.source = current
	e.info = newInfo
	e.checkHealth = func(cfg settings.Config, info buildinfo.Info) error {
		if err := health(cfg, info); err != nil {
			return err
		}
		if info.Version == newInfo.Version {
			if v, err := storage.InspectVersion(filepath.Join(root, "data")); err != nil || v != 2 {
				return errors.New("新程序未实际完成格式迁移")
			}
			return errors.New("注入格式迁移后的启动验收失败")
		}
		return nil
	}
	if err = e.apply(); err == nil || !strings.Contains(err.Error(), "已恢复旧版本") {
		t.Fatal("真实跨格式失败未恢复旧版本", err)
	}
	if version, err := storage.InspectVersion(filepath.Join(root, "data")); err != nil || version != 1 {
		t.Fatal("回退后未恢复原格式1数据", version, err)
	}
	if _, err = os.Stat(filepath.Join(root, "data", secretbox.KeyFile)); !os.IsNotExist(err) {
		t.Fatal("新密钥遗留在格式1恢复目录")
	}
	failedKeys, _ := filepath.Glob(filepath.Join(root, "backups", "*", "failed-data", secretbox.KeyFile))
	if len(failedKeys) != 1 {
		t.Fatal("迁移失败的密钥现场未保留")
	}
	call("GET", "/projects/legacy-live/docs", nil, oldRaw, 200)
	e.checkHealth = health
	if err = e.apply(); err != nil {
		t.Fatal("格式1升级当前格式失败", err)
	}
	call("GET", "/projects/legacy-live/docs", nil, oldRaw, 200)
	path := "/admin/projects/" + pid + "/tokens/" + oldID
	call("POST", path+"/reveal", map[string]any{}, "", 409)
	call("PUT", path+"/secret", map[string]any{"secret": oldRaw}, "", 200)
	if recovered := call("POST", path+"/reveal", map[string]any{}, "", 200); recovered["secret"] != oldRaw {
		t.Fatal("旧Token补存改变原凭据")
	}
	newToken := call("POST", "/admin/projects/"+pid+"/tokens", map[string]any{"name": "加密新Token"}, "", 201)
	newRaw := newToken["secret"].(string)
	newID := newToken["token"].(map[string]any)["id"].(string)
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(base, "paired-backup")
	if err = copyTree(filepath.Join(root, "data"), backup); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "data", secretbox.KeyFile)
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	failedContext, cancelFailed := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelFailed()
	badStart := exec.CommandContext(failedContext, current, "serve", "--config", filepath.Join(root, "config.yaml"))
	var stderr bytes.Buffer
	badStart.Stderr = &stderr
	if err = badStart.Run(); err == nil || !strings.Contains(stderr.String(), "token.key") {
		t.Fatal("丢失密钥时未明确拒绝启动")
	}
	if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("现有密钥被悄悄换新")
	}
	if err = copyFile(filepath.Join(backup, secretbox.KeyFile), keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err = ctl("start"); err != nil {
		t.Fatal(err)
	}
	cfg, err := settings.Load(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err = health(cfg, newInfo); err != nil {
		t.Fatal(err)
	}
	if got := call("POST", "/admin/projects/"+pid+"/tokens/"+newID+"/reveal", map[string]any{}, "", 200); got["secret"] != newRaw {
		t.Fatal("配套密钥恢复后不能还原原Token")
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	oldContext, cancelOld := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelOld()
	oldStart := exec.CommandContext(oldContext, legacy, "serve", "--config", filepath.Join(root, "config.yaml"))
	stderr.Reset()
	oldStart.Stderr = &stderr
	if err = oldStart.Run(); err == nil || !strings.Contains(stderr.String(), "数据库版本") {
		t.Fatal("真实旧程序未拒绝当前格式数据库")
	}
}
