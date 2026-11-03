// Package installer负责固定目录部署。测试用临时目录及替代服务管理器执行相同事务流程。
package installer

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devconsole/deploy"
	"devconsole/internal/buildinfo"
	"devconsole/internal/settings"
	"devconsole/internal/storage"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const installRoot = "/opt/dconsole"
const serviceName = "dconsole.service"

type engine struct {
	root, unitPath, source string
	info                   buildinfo.Info
	reinstall              bool
	serialGroup            string
	port                   int
	portSpecified          bool
	choosePort             func() (int, error)
	uid, gid               int
	prepare                func() error
	systemctl              func(...string) error
	bootstrap              func(string, string) error
	chown                  func(string, int, int) error
	checkHealth            func(settings.Config, buildinfo.Info) error
	output                 io.Writer
}

func Run(args []string) error {
	f := flag.NewFlagSet("install", flag.ContinueOnError)
	reinstall := f.Bool("reinstall", false, "同版本修复安装，保留配置和数据")
	group := f.String("serial-group", "", "首次安装时授予服务的实际串口组")
	stdin := f.Bool("password-stdin", false, "首次安装时从标准输入读取管理员密码")
	portText := f.String("port", "", "首次安装HTTP端口，1～65535；交互默认8080，非交互必须指定")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("安装参数包含未知位置参数")
	}
	portSpecified := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "port" {
			portSpecified = true
		}
	})
	selectedPort := 0
	if portSpecified {
		var err error
		selectedPort, err = parsePort(*portText)
		if err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("仅支持Linux/amd64")
	}
	if os.Geteuid() != 0 {
		return errors.New("安装需要root权限，请使用sudo bash安装包；--extract仅解压无需root")
	}
	if _, err := buildinfo.Parse(buildinfo.Version); err != nil {
		return err
	}
	if *group != "" {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*[$]?$`).MatchString(*group) {
			return errors.New("串口组名非法")
		}
		if _, err := user.LookupGroup(*group); err != nil {
			return fmt.Errorf("串口组不存在：%s", *group)
		}
	}
	for _, cmd := range []string{"systemctl", "useradd", "runuser"} {
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf("缺少系统命令%s，请由系统管理员安装", cmd)
		}
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("目标机器需要运行systemd系统管理器")
	}
	versionOutput, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return err
	}
	fields := strings.Fields(string(versionOutput))
	if len(fields) < 2 {
		return errors.New("无法识别systemd版本")
	}
	systemdVersion, err := strconv.Atoi(fields[1])
	if err != nil || systemdVersion < 240 {
		return errors.New("需要systemd 240或以上版本以支持目录内日志")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	run := func(args ...string) error {
		cctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		cmd := exec.CommandContext(cctx, "systemctl", args...)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s失败：%s：%w", strings.Join(args, " "), strings.TrimSpace(string(b)), err)
		}
		return nil
	}
	if err := run("show", "--property=SystemState", "--value"); err != nil {
		return err
	}
	lock, err := acquireLock("/run/lock/dconsole-install.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	source, err := os.Executable()
	if err != nil {
		return err
	}
	e := &engine{root: installRoot, unitPath: "/etc/systemd/system/" + serviceName, source: source, info: buildinfo.Current(), reinstall: *reinstall, serialGroup: *group, systemctl: run, chown: os.Chown, checkHealth: health, output: os.Stdout}
	e.portSpecified = portSpecified
	e.choosePort = func() (int, error) { return initialPort(selectedPort, portSpecified, *stdin) }
	e.prepare = func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		u, err := user.Lookup("dconsole")
		if err != nil {
			if _, ok := err.(user.UnknownUserError); !ok {
				return err
			}
			cmd := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--home-dir", installRoot, "--no-create-home", "--shell", "/usr/sbin/nologin", "dconsole")
			if b, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("创建服务用户失败：%s：%w", b, err)
			}
			u, err = user.Lookup("dconsole")
			if err != nil {
				return err
			}
		}
		e.uid, err = strconv.Atoi(u.Uid)
		if err != nil || e.uid == 0 {
			return errors.New("dconsole必须是普通服务用户")
		}
		e.gid, err = strconv.Atoi(u.Gid)
		if err != nil {
			return err
		}
		if u.HomeDir != installRoot {
			return errors.New("已有dconsole账号的主目录不匹配，未复用该账号")
		}
		g, err := user.LookupGroup("dconsole")
		if err != nil || g.Gid != u.Gid {
			return errors.New("dconsole账号必须使用同名主组")
		}
		return nil
	}
	e.bootstrap = func(binary, data string) error {
		if !*stdin && !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("首次安装需要交互终端设置管理员密码；自动化须显式使用--password-stdin")
		}
		argv := []string{"-u", "dconsole", "--", binary, "admin", "bootstrap", "--data-dir", data, "--username", "admin"}
		if *stdin {
			argv = append(argv, "--password-stdin")
		}
		cmd := exec.CommandContext(ctx, "runuser", argv...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return e.apply()
}

func acquireLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("另一个安装进程正在执行")
	}
	return f, nil
}

func readInfo(binary string) (buildinfo.Info, error) {
	var info buildinfo.Info
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return info, fmt.Errorf("无法读取已安装程序版本：%w", err)
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if _, err = buildinfo.Parse(info.Version); err != nil {
		return info, err
	}
	if info.Target != "linux/amd64" || info.DatabaseMin < 1 || info.DatabaseMax < info.DatabaseMin {
		return info, errors.New("已安装程序的版本描述不受支持")
	}
	return info, nil
}

func (e *engine) apply() error {
	if _, err := buildinfo.Parse(e.info.Version); err != nil {
		return err
	}
	if err := e.checkUnit(); err != nil {
		return err
	}
	if err := e.checkPath(filepath.Dir(e.root)); err != nil {
		return err
	}
	st, err := os.Lstat(e.root)
	if os.IsNotExist(err) {
		e.port = 8080
		if e.choosePort != nil {
			e.port, err = e.choosePort()
			if err != nil {
				return err
			}
		}
		if err = portAvailable(e.port); err != nil {
			return err
		}
		if err = e.prepare(); err != nil {
			return err
		}
		return e.first()
	}
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("安装目录必须是实际目录，不能是符号链接")
	}
	if e.portSpecified {
		return errors.New("--port仅用于首次安装；已有端口保留，修改请编辑config.yaml并重启服务")
	}
	for _, p := range []string{e.root, e.binary(), e.config(), filepath.Join(e.root, serviceName)} {
		if err = e.checkPath(p); err != nil {
			return err
		}
	}
	old, err := readInfo(e.binary())
	if err != nil {
		return err
	}
	order, err := buildinfo.Compare(e.info.Version, old.Version)
	if err != nil {
		return err
	}
	if order < 0 {
		return fmt.Errorf("拒绝降级：已安装%s，安装包%s；请按备份恢复流程处理", old.Version, e.info.Version)
	}
	if e.serialGroup != "" {
		return errors.New("已安装实例保留原服务定义，不能通过安装参数修改串口组；请编辑目录内服务文件")
	}
	if order == 0 && !e.reinstall {
		if _, err = os.Lstat(e.unitPath); err != nil {
			return errors.New("同版本安装不完整，请使用--reinstall修复")
		}
		fmt.Fprintf(e.output, "已安装%s，无需操作；需要修复时使用--reinstall\n", old.Version)
		return nil
	}
	cfg, err := settings.Load(e.config())
	if err != nil {
		return err
	}
	data := cfg.DataDir
	if !filepath.IsAbs(data) {
		data = filepath.Join(e.root, data)
	}
	if filepath.Clean(data) != filepath.Join(e.root, "data") {
		return errors.New("安装管理的数据目录必须为/opt/dconsole/data，未修改现有配置")
	}
	if err = e.checkDirectory(filepath.Join(e.root, "data")); err != nil {
		return err
	}
	schema, err := storage.InspectVersion(data)
	if err != nil {
		return fmt.Errorf("只读数据库预检失败：%w", err)
	}
	if schema < e.info.DatabaseMin || schema > e.info.DatabaseMax {
		return fmt.Errorf("安装包不支持当前数据库格式%d", schema)
	}
	if err = e.prepare(); err != nil {
		return err
	}
	return e.upgrade(cfg, old)
}

func (e *engine) binary() string { return filepath.Join(e.root, "dconsole") }
func (e *engine) config() string { return filepath.Join(e.root, "config.yaml") }
func (e *engine) checkPath(p string) error {
	s, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if s.Mode()&os.ModeSymlink != 0 || (!s.IsDir() && !s.Mode().IsRegular()) {
		return fmt.Errorf("受管理路径不能是链接或特殊文件：%s", p)
	}
	return nil
}
func (e *engine) checkDirectory(p string) error {
	s, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("需要实际目录：%s", p)
	}
	return nil
}
func (e *engine) checkUnit() error {
	s, err := os.Lstat(e.unitPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Mode()&os.ModeSymlink == 0 {
		return errors.New("系统中已有其他dconsole.service，未覆盖")
	}
	target, err := os.Readlink(e.unitPath)
	if err != nil {
		return err
	}
	if target != filepath.Join(e.root, serviceName) {
		return errors.New("系统dconsole.service指向其他位置，未覆盖")
	}
	return nil
}

func (e *engine) first() (err error) {
	stage, err := os.MkdirTemp(filepath.Dir(e.root), ".dconsole-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = os.Chmod(stage, 0755); err != nil {
		return err
	}
	for _, name := range []string{"data", "logs"} {
		p := filepath.Join(stage, name)
		if err = os.Mkdir(p, 0700); err != nil {
			return err
		}
		if err = e.chown(p, e.uid, e.gid); err != nil {
			return err
		}
	}
	if err = copyFile(e.source, filepath.Join(stage, "dconsole"), 0755); err != nil {
		return err
	}
	cfg := fmt.Sprintf("listen: \"0.0.0.0:%d\"\npublic_base_url: \"\"\ndata_dir: %q\n", e.port, filepath.Join(e.root, "data"))
	if err = os.WriteFile(filepath.Join(stage, "config.yaml"), []byte(cfg), 0640); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(stage, "config.yaml"), 0640); err != nil {
		return err
	}
	if err = e.chown(filepath.Join(stage, "config.yaml"), 0, e.gid); err != nil {
		return err
	}
	unit := e.serviceDefinition()
	if err = os.WriteFile(filepath.Join(stage, serviceName), []byte(unit), 0644); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(stage, serviceName), 0644); err != nil {
		return err
	}
	if err = e.bootstrap(filepath.Join(stage, "dconsole"), filepath.Join(stage, "data")); err != nil {
		return fmt.Errorf("初始化未完成，未安装服务：%w", err)
	}
	if _, statErr := os.Lstat(e.root); !os.IsNotExist(statErr) {
		return errors.New("安装目录在操作期间已出现，未覆盖")
	}
	if err = os.Rename(stage, e.root); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			stopErr := e.systemctl("stop", serviceName)
			err = fmt.Errorf("首次安装未完成，现场保留在%s；修复原因后使用--reinstall继续：%w；停止服务结果：%v", e.root, err, stopErr)
		}
	}()
	if err = e.linkUnit(); err != nil {
		return err
	}
	if err = e.systemctl("daemon-reload"); err != nil {
		return err
	}
	if err = e.systemctl("start", serviceName); err != nil {
		return err
	}
	loaded, err := settings.Load(e.config())
	if err != nil {
		return err
	}
	if err = e.checkHealth(loaded, e.info); err != nil {
		return err
	}
	if err = e.systemctl("is-active", "--quiet", serviceName); err != nil {
		return err
	}
	if err = e.systemctl("enable", serviceName); err != nil {
		return err
	}
	fmt.Fprintf(e.output, "安装成功：%s，版本%s；访问%s\n", e.root, e.info.Version, loaded.PublicBaseURL)
	return nil
}

func (e *engine) linkUnit() error {
	if err := e.checkUnit(); err != nil {
		return err
	}
	if _, err := os.Lstat(e.unitPath); err == nil {
		return nil
	}
	return os.Symlink(filepath.Join(e.root, serviceName), e.unitPath)
}

func (e *engine) serviceDefinition() string {
	unit := strings.ReplaceAll(deploy.Service, installRoot, e.root)
	if e.serialGroup != "" {
		unit = strings.Replace(unit, "Group=dconsole\n", "Group=dconsole\nSupplementaryGroups="+e.serialGroup+"\n", 1)
	}
	if e.port < 1024 {
		unit = strings.Replace(unit, "NoNewPrivileges=true\n", "NoNewPrivileges=true\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n", 1)
	}
	return unit
}

func (e *engine) upgrade(cfg settings.Config, old buildinfo.Info) (err error) {
	backups := filepath.Join(e.root, "backups")
	if err = os.Mkdir(backups, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err = e.checkDirectory(backups); err != nil {
		return err
	}
	if err = e.checkPath(backups); err != nil {
		return err
	}
	// 首装服务登记失败后，--reinstall可在保留数据库的情况下补齐链接。
	if _, statErr := os.Lstat(e.unitPath); os.IsNotExist(statErr) {
		if err = e.linkUnit(); err != nil {
			return err
		}
		if err = e.systemctl("daemon-reload"); err != nil {
			return err
		}
	}
	if err = e.systemctl("stop", serviceName); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(backups, time.Now().UTC().Format("20060102T150405Z")+"-"+old.Version+"-")
	if err != nil {
		startErr := e.systemctl("start", serviceName)
		return errors.Join(err, startErr)
	}
	backedUp, replaced := false, false
	defer func() {
		if err == nil {
			return
		}
		if !replaced {
			restartErr := e.systemctl("start", serviceName)
			if restartErr == nil {
				restartErr = e.checkHealth(cfg, old)
			}
			err = errors.Join(err, restartErr)
			return
		}
		if stopErr := e.systemctl("stop", serviceName); stopErr != nil {
			err = fmt.Errorf("升级失败且无法停止新服务，未恢复数据；备份%s：%w", backup, errors.Join(err, stopErr))
			return
		}
		if backedUp {
			if restoreErr := e.restore(backup, old); restoreErr != nil {
				err = fmt.Errorf("升级失败，自动恢复未完成，请使用备份%s：%w", backup, errors.Join(err, restoreErr))
				return
			}
		}
		restartErr := e.systemctl("start", serviceName)
		if restartErr == nil {
			restartErr = e.checkHealth(cfg, old)
		}
		if restartErr != nil {
			err = fmt.Errorf("已恢复旧文件但服务恢复未确认，备份%s：%w", backup, errors.Join(err, restartErr))
		} else {
			err = fmt.Errorf("升级失败，已恢复旧版本%s；备份%s：%w", old.Version, backup, err)
		}
	}()
	for _, name := range []string{"dconsole", "config.yaml", serviceName} {
		if err = copyFile(filepath.Join(e.root, name), filepath.Join(backup, name), 0600); err != nil {
			return err
		}
	}
	if err = copyTree(filepath.Join(e.root, "data"), filepath.Join(backup, "data")); err != nil {
		return err
	}
	backedUp = true
	stageDir, err := os.MkdirTemp(e.root, ".binary-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	stage := filepath.Join(stageDir, "dconsole")
	if err = copyFile(e.source, stage, 0755); err != nil {
		return err
	}
	if err = os.Rename(stage, e.binary()); err != nil {
		return err
	}
	replaced = true
	if err = e.linkUnit(); err != nil {
		return err
	}
	if err = e.systemctl("daemon-reload"); err != nil {
		return err
	}
	if err = e.systemctl("start", serviceName); err != nil {
		return err
	}
	if err = e.checkHealth(cfg, e.info); err != nil {
		return err
	}
	if err = e.systemctl("is-active", "--quiet", serviceName); err != nil {
		return err
	}
	if err = e.systemctl("enable", serviceName); err != nil {
		return err
	}
	fmt.Fprintf(e.output, "升级成功：%s → %s；配置和数据保留；备份%s\n", old.Version, e.info.Version, backup)
	return nil
}

func (e *engine) restore(backup string, old buildinfo.Info) error {
	// 若新程序已改变数据库格式，旧程序必须配套恢复整份停机数据，不能只换二进制。
	data := filepath.Join(e.root, "data")
	version, err := storage.InspectVersion(data)
	if err != nil || version < old.DatabaseMin || version > old.DatabaseMax {
		staged := filepath.Join(backup, "restored-data")
		if err = copyTree(filepath.Join(backup, "data"), staged); err != nil {
			return err
		}
		if err = filepath.WalkDir(staged, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return e.chown(p, e.uid, e.gid)
		}); err != nil {
			return err
		}
		// 原子交换目录，失败时原数据目录仍在原位，避免恢复中途留下空路径。
		if err = unix.Renameat2(unix.AT_FDCWD, data, unix.AT_FDCWD, staged, unix.RENAME_EXCHANGE); err != nil {
			return err
		}
		if moveErr := os.Rename(staged, filepath.Join(backup, "failed-data")); moveErr != nil {
			fmt.Fprintf(e.output, "数据已恢复，新版数据现场保留在%s：%v\n", staged, moveErr)
		}
	}
	stageDir, err := os.MkdirTemp(e.root, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	stage := filepath.Join(stageDir, "dconsole")
	if err = copyFile(filepath.Join(backup, "dconsole"), stage, 0755); err != nil {
		return err
	}
	return os.Rename(stage, e.binary())
}

func health(cfg settings.Config, expected buildinfo.Info) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	endpoint := "http://" + net.JoinHostPort(host, port) + "/healthz"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(endpoint)
		if err == nil {
			var got struct{ Status, Version, Commit string }
			decodeErr := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&got)
			res.Body.Close()
			if res.StatusCode == 200 && decodeErr == nil && got.Status == "ok" && got.Version == expected.Version && got.Commit == expected.Commit {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("新服务未在30秒内通过版本匹配的健康检查：%s", endpoint)
}
