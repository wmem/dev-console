// Package testdevice提供独立协议模拟器，不引用生产驱动的编解码或CRC实现。
package testdevice

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Protocol         string
	Delay            time.Duration
	Chunk            int
	Corrupt          bool
	DropAfterControl bool
	ReplyOverride    map[string]string
	Seen             chan string
	Gate             <-chan struct{}
}
type State struct {
	mu      sync.Mutex
	V, I    float64
	Enabled bool
	Writes  int
}

func NewState() *State { return &State{V: 12, I: 1} }
func (s *State) Snapshot() (float64, float64, bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.V, s.I, s.Enabled, s.Writes
}
func Serve(conn io.ReadWriteCloser, s *State, opt Options) {
	defer conn.Close()
	if opt.Protocol == "modbus" {
		serveModbus(conn, s, opt)
	} else {
		serveSCPI(conn, s, opt)
	}
}
func reply(conn io.Writer, b []byte, opt Options) error {
	if opt.Gate != nil {
		<-opt.Gate
	}
	if opt.Delay > 0 {
		time.Sleep(opt.Delay)
	}
	if opt.Corrupt && len(b) > 0 {
		b[len(b)-1] ^= 0xff
	}
	chunk := opt.Chunk
	if chunk <= 0 {
		chunk = len(b)
	}
	for len(b) > 0 {
		n := min(chunk, len(b))
		if _, e := conn.Write(b[:n]); e != nil {
			return e
		}
		b = b[n:]
	}
	return nil
}
func seen(opt Options, cmd string) {
	if opt.Seen != nil {
		select {
		case opt.Seen <- cmd:
		default:
		}
	}
}
func serveSCPI(conn io.ReadWriteCloser, s *State, opt Options) {
	reader := bufio.NewReader(conn)
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		seen(opt, cmd)
		response := ""
		control := false
		s.mu.Lock()
		switch cmd {
		case "*IDN?":
			response = "ITECH Ltd., IT6722, SIMULATOR, 1.0"
		case "VOLT?":
			response = fmt.Sprint(s.V)
		case "CURR?":
			response = fmt.Sprint(s.I)
		case "OUTP?":
			response = "0"
			if s.Enabled {
				response = "1"
			}
		case "MEAS:VOLT?":
			response = "0"
			if s.Enabled {
				response = fmt.Sprint(s.V)
			}
		case "MEAS:CURR?":
			response = "1e-04"
		case "SYST:ERR?":
			response = "0, \"No error\""
		case "SYST:REM", "SYST:LOC":
		default:
			parts := strings.Fields(cmd)
			if len(parts) == 2 {
				v, err := strconv.ParseFloat(parts[1], 64)
				if err == nil {
					switch parts[0] {
					case "VOLT":
						s.V = v
						control = true
					case "CURR":
						s.I = v
						control = true
					case "OUTP":
						s.Enabled = v == 1
						control = true
					}
					if control {
						s.Writes++
					}
				}
			}
		}
		s.mu.Unlock()
		if control && opt.DropAfterControl {
			return
		}
		if override, ok := opt.ReplyOverride[cmd]; ok {
			response = override
		}
		if response != "" {
			if e = reply(conn, []byte(response+"\n"), opt); e != nil {
				return
			}
		}
	}
}

// fixtureCRC采用逐位长除法，与生产函数保持独立，历史帧用于另行核对。
func fixtureCRC(data []byte) []byte {
	crc := uint32(65535)
	for _, v := range data {
		crc = crc ^ uint32(v)
		for j := 0; j < 8; j++ {
			lsb := crc % 2
			crc /= 2
			if lsb != 0 {
				crc ^= 40961
			}
		}
	}
	return []byte{byte(crc), byte(crc / 256)}
}
func serveModbus(conn io.ReadWriteCloser, s *State, opt Options) {
	for {
		p := make([]byte, 8)
		if _, e := io.ReadFull(conn, p); e != nil {
			return
		}
		seen(opt, fmt.Sprintf("%x", p))
		reg := binary.BigEndian.Uint16(p[2:4])
		value := binary.BigEndian.Uint16(p[4:6])
		response := []byte{}
		s.mu.Lock()
		if p[1] == 6 {
			switch reg {
			case 4096:
				s.V = float64(value) / 100
			case 4097:
				s.I = float64(value) / 100
			case 4102:
				s.Enabled = value == 1
			}
			s.Writes++
			response = append(response, p...)
		} else if p[1] == 3 && reg == 4096 && value == 6 {
			enabled := uint16(0)
			voltage := uint16(0)
			if s.Enabled {
				enabled = 1
				voltage = uint16(s.V*100 + 0.5)
			}
			regs := []uint16{uint16(s.V*100 + 0.5), uint16(s.I*100 + 0.5), voltage, 0, enabled, 1}
			response = []byte{p[0], 3, 12}
			for _, v := range regs {
				response = append(response, byte(v/256), byte(v%256))
			}
			response = append(response, fixtureCRC(response)...)
		} else {
			response = []byte{p[0], p[1] | 128, 2}
			response = append(response, fixtureCRC(response)...)
		}
		s.mu.Unlock()
		if p[1] == 6 && opt.DropAfterControl {
			return
		}
		if e := reply(conn, response, opt); e != nil {
			return
		}
	}
}

type Port struct {
	net.Conn
	Timeout time.Duration
}

func (p *Port) SetReadTimeout(d time.Duration) error { p.Timeout = d; return nil }
func (p *Port) Read(b []byte) (int, error) {
	_ = p.Conn.SetReadDeadline(time.Now().Add(p.Timeout))
	n, e := p.Conn.Read(b)
	if ne, ok := e.(net.Error); ok && ne.Timeout() {
		return n, nil
	}
	return n, e
}
