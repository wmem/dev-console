package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	c "devconsole/internal/contracts"
	"go.bug.st/serial"
)

type Config struct {
	Path       string  `json:"path"`
	Baud       int     `json:"baud_rate"`
	DataBits   int     `json:"data_bits"`
	Parity     string  `json:"parity"`
	StopBits   int     `json:"stop_bits"`
	TimeoutMS  int     `json:"timeout_ms"`
	Address    int     `json:"address,omitempty"`
	MaxVoltage float64 `json:"max_voltage"`
	MaxCurrent float64 `json:"max_current"`
}

func (cfg *Config) Normalize() error {
	if cfg.Path == "" {
		return c.Fail("INVALID_ARGUMENT", "串口路径不能为空")
	}
	if strings.HasPrefix(strings.ToUpper(cfg.Path), "COM") {
		cfg.Path = strings.ToUpper(cfg.Path)
	} else {
		p, e := filepath.Abs(cfg.Path)
		if e != nil {
			return e
		}
		cfg.Path = filepath.Clean(p)
	}
	if cfg.Baud == 0 {
		cfg.Baud = 9600
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.Parity == "" {
		cfg.Parity = "none"
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = 1
	}
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = 1500
	}
	if cfg.Baud < 300 || cfg.Baud > 921600 || cfg.DataBits < 5 || cfg.DataBits > 8 || (cfg.Parity != "none" && cfg.Parity != "even" && cfg.Parity != "odd") || (cfg.StopBits != 1 && cfg.StopBits != 2) || cfg.TimeoutMS < 100 || cfg.TimeoutMS > 10000 {
		return c.Fail("INVALID_ARGUMENT", "串口参数或超时范围非法")
	}
	return nil
}

// EndpointPath只用于本次连接定位和共享资源键，不能覆盖用户保存的稳定路径。
func EndpointPath(path string) string {
	if resolved, e := filepath.EvalSymlinks(path); e == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
func (a Config) SameBus(b Config) bool {
	return a.Path == b.Path && a.Baud == b.Baud && a.DataBits == b.DataBits && a.Parity == b.Parity && a.StopBits == b.StopBits && a.TimeoutMS == b.TimeoutMS
}

type Port interface {
	io.ReadWriteCloser
	SetReadTimeout(time.Duration) error
}
type Opener func(Config) (Port, error)

func Open(cfg Config) (Port, error) {
	parity := serial.NoParity
	if cfg.Parity == "even" {
		parity = serial.EvenParity
	} else if cfg.Parity == "odd" {
		parity = serial.OddParity
	}
	stop := serial.OneStopBit
	if cfg.StopBits == 2 {
		stop = serial.TwoStopBits
	}
	return serial.Open(cfg.Path, &serial.Mode{BaudRate: cfg.Baud, DataBits: cfg.DataBits, Parity: parity, StopBits: stop})
}

type Bus struct {
	Config     Config
	Busy       atomic.Bool
	port       Port
	retired    bool
	open       Opener
	quarantine time.Time
}

func New(cfg Config, open Opener) *Bus {
	if open == nil {
		open = Open
	}
	return &Bus{Config: cfg, open: open}
}
func (b *Bus) Acquire() bool { return b.Busy.CompareAndSwap(false, true) }
func (b *Bus) Release()      { b.Busy.Store(false) }
func (b *Bus) CloseHeld() {
	if b.port != nil {
		_ = b.port.Close()
		b.port = nil
	}
}
func (b *Bus) RetireHeld() { b.CloseHeld(); b.retired = true }

type Wire struct {
	Context context.Context
	Port    Port
	Sent    bool
	Trace   func(string, []byte)
}

func (w *Wire) Write(p []byte, control bool) error {
	if e := w.Context.Err(); e != nil {
		return e
	}
	if control {
		w.Sent = true
	}
	for len(p) > 0 {
		n, e := w.Port.Write(p)
		if n > 0 && w.Trace != nil {
			w.Trace("tx", append([]byte{}, p[:n]...))
		}
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func (w *Wire) Exact(n int) ([]byte, error) {
	b := make([]byte, n)
	pos := 0
	for pos < n {
		if e := w.Context.Err(); e != nil {
			return nil, e
		}
		count, e := w.Port.Read(b[pos:])
		if count > 0 && w.Trace != nil {
			w.Trace("rx", append([]byte{}, b[pos:pos+count]...))
		}
		pos += count
		if e != nil {
			return nil, e
		}
	}
	return b, nil
}
func (w *Wire) Line() (string, error) {
	out := []byte{}
	for len(out) <= 1024 {
		p, e := w.Exact(1)
		if e != nil {
			return "", e
		}
		if p[0] == '\n' {
			s := strings.TrimSuffix(string(out), "\r")
			if s == "" {
				return "", c.Fail("PROTOCOL_ERROR", "收到空回复")
			}
			return s, nil
		}
		if p[0] > 127 || p[0] == 0 {
			return "", c.Fail("PROTOCOL_ERROR", "收到非ASCII回复")
		}
		out = append(out, p[0])
	}
	return "", c.Fail("PROTOCOL_ERROR", "设备回复超过1024字节")
}

// RunHeld要求调用方已非阻塞获取busy；超时清理和隔离期间保持busy，防止旧回复污染下一次调用。
func (b *Bus) RunHeld(ctx context.Context, fn func(*Wire) error, trace func(string, []byte)) (sent bool, err error) {
	if b.retired {
		return false, c.Fail("DEVICE_OFFLINE", "连接配置已更换")
	}
	if time.Now().Before(b.quarantine) {
		return false, c.Fail("DEVICE_OFFLINE", "连接等待旧回复隔离，请稍后再调用")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(b.Config.TimeoutMS)*time.Millisecond)
	defer cancel()
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if b.port == nil {
		p, e := b.open(b.Config)
		if e != nil {
			return false, c.Fail("DEVICE_OFFLINE", fmt.Sprintf("无法打开串口：%v", e))
		}
		b.port = p
		if resetter, ok := p.(interface{ ResetInputBuffer() error }); ok {
			if e = resetter.ResetInputBuffer(); e != nil {
				b.CloseHeld()
				return false, e
			}
		}
		if e = p.SetReadTimeout(50 * time.Millisecond); e != nil {
			b.CloseHeld()
			return false, e
		}
	}
	p := b.port
	done := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	closePort := func() { once.Do(func() { _ = p.Close() }) }
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			closePort()
		case <-done:
		}
	}()
	w := &Wire{Context: ctx, Port: p, Trace: trace}
	err = fn(w)
	close(done)
	<-stopped
	sent = w.Sent
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		closePort()
		b.port = nil
		b.quarantine = time.Now().Add(time.Duration(b.Config.TimeoutMS) * time.Millisecond)
	}
	return sent, err
}
func MapError(err error, sent bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if sent {
			return &c.Error{Code: "OUTCOME_UNKNOWN", Message: "命令已发送，但确认未完成", Outcome: "unknown"}
		}
		return c.Fail("TIMEOUT", "读取超时或请求已取消")
	}
	var ce *c.Error
	if errors.As(err, &ce) {
		if sent && ce.Outcome == "not_started" {
			return &c.Error{Code: ce.Code, Message: ce.Message, Outcome: "unknown"}
		}
		return err
	}
	if sent {
		return &c.Error{Code: "OUTCOME_UNKNOWN", Message: "通信中断，控制结果未知：" + err.Error(), Outcome: "unknown"}
	}
	return c.Fail("PROTOCOL_ERROR", "通信失败："+err.Error())
}
