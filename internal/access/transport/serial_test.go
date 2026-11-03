package transport

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

type cleanupPort struct {
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

func (p *cleanupPort) Read(b []byte) (int, error)         { time.Sleep(5 * time.Millisecond); return 0, nil }
func (p *cleanupPort) Write(b []byte) (int, error)        { return len(b), nil }
func (p *cleanupPort) SetReadTimeout(time.Duration) error { return nil }
func (p *cleanupPort) Close() error                       { p.once.Do(func() { close(p.started); <-p.allow }); return nil }
func TestBusyHeldUntilCancellationCleanup(t *testing.T) {
	port := &cleanupPort{started: make(chan struct{}), allow: make(chan struct{})}
	bus := New(Config{TimeoutMS: 100}, func(Config) (Port, error) { return port, nil })
	if !bus.Acquire() {
		t.Fatal("未能获取初始busy")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer bus.Release()
		_, _ = bus.RunHeld(context.Background(), func(w *Wire) error { _, e := w.Exact(1); return e }, nil)
	}()
	select {
	case <-port.started:
	case <-time.After(time.Second):
		t.Fatal("未进行超时关闭")
	}
	if bus.Acquire() {
		t.Fatal("旧连接清理前释放了busy")
	}
	select {
	case <-done:
		t.Fatal("清理未完成调用却已返回")
	default:
	}
	close(port.allow)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("清理完成后未释放")
	}
	if !bus.Acquire() {
		t.Fatal("清理后busy未释放")
	}
	defer bus.Release()
	if _, e := bus.RunHeld(context.Background(), func(w *Wire) error { return io.EOF }, nil); e == nil {
		t.Fatal("旧回复隔离期间未拒绝连接")
	}
}
