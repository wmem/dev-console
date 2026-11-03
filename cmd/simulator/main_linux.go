// Linux伪终端模拟器供浏览器与完整HTTP端到端验收使用。
package main

import (
	"devconsole/internal/testdevice"
	"flag"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/signal"
	"time"
)

func main() {
	protocol := flag.String("protocol", "scpi", "scpi或modbus")
	delay := flag.Duration("delay", 0, "每次回复延迟")
	flag.Parse()
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if e != nil {
		panic(e)
	}
	if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
		panic(e)
	}
	n, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if e != nil {
		panic(e)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	slave, e := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if e != nil {
		panic(e)
	}
	defer slave.Close()
	master := os.NewFile(uintptr(fd), "pty-master")
	fmt.Println(path)
	go testdevice.Serve(master, testdevice.NewState(), testdevice.Options{Protocol: *protocol, Delay: *delay, Chunk: 3})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, unix.SIGTERM)
	select {
	case <-signals:
	case <-time.After(24 * time.Hour):
	}
	master.Close()
}
