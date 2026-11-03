package power

import (
	"context"
	"devconsole/internal/access/transport"
	"devconsole/internal/testdevice"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHistoricalFrames(t *testing.T) {
	if CRC([]byte("123456789")) != 0x4b37 {
		t.Fatal("标准CRC向量错误")
	}
	cases := []struct {
		fn   byte
		r, v uint16
		hex  string
	}{{3, 0x1000, 6, "010310000006c108"}, {6, 0x1000, 1200, "0106100004b08e7e"}, {6, 0x1001, 100, "010610010064dd21"}, {6, 0x1006, 0, "0106100600006d0b"}, {6, 0x1006, 1, "010610060001accb"}}
	for _, tc := range cases {
		if got := hex.EncodeToString(Frame(1, tc.fn, tc.r, tc.v)); got != tc.hex {
			t.Fatalf("%s != %s", got, tc.hex)
		}
	}
	rx, _ := hex.DecodeString("01030c04b001f404b00000000100017344")
	if CRC(rx[:len(rx)-2]) != 0x4473 {
		t.Fatal("历史回复CRC错误")
	}
}
func runWire(t *testing.T, opt testdevice.Options, key string, args map[string]any) (bool, error) {
	t.Helper()
	a, b := net.Pipe()
	go testdevice.Serve(b, testdevice.NewState(), opt)
	bus := transport.New(transport.Config{TimeoutMS: 400}, func(transport.Config) (transport.Port, error) { return &testdevice.Port{Conn: a}, nil })
	bus.Acquire()
	defer bus.Release()
	defer bus.CloseHeld()
	return bus.RunHeld(context.Background(), func(w *transport.Wire) error {
		if opt.Protocol == "modbus" {
			_, e := Longwei(w, transport.Config{Address: 1}, key, args)
			return e
		}
		_, e := Itech(w, transport.Config{}, key, args)
		return e
	}, nil)
}
func TestProtocolFragmentation(t *testing.T) {
	for _, protocol := range []string{"scpi", "modbus"} {
		for _, step := range []struct {
			key  string
			args map[string]any
		}{{"settings.read", map[string]any{}}, {"measurement.read", map[string]any{}}, {"output.read", map[string]any{}}, {"voltage.set", map[string]any{"voltage": .29}}, {"current_limit.set", map[string]any{"current_limit": .5}}, {"output.set", map[string]any{"enabled": true}}} {
			t.Run(protocol+"/"+step.key, func(t *testing.T) {
				_, e := runWire(t, testdevice.Options{Protocol: protocol, Chunk: 1}, step.key, step.args)
				if e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}
func TestSCPIInvalidReplies(t *testing.T) {
	for _, tc := range []struct{ cmd, reply string }{{"*IDN?", "UNKNOWN, Other, 1"}, {"VOLT?", "NaN"}, {"VOLT?", " "},
		{"VOLT?", "\n"}, {"VOLT?", string([]byte{0xff})}, {"VOLT?", strings.Repeat("1", 1100)}, {"SYST:ERR?", "-100, \"error\""}} {
		t.Run(tc.cmd+tc.reply[:min(5, len(tc.reply))], func(t *testing.T) {
			key := "settings.read"
			args := map[string]any{}
			if tc.cmd == "SYST:ERR?" {
				key = "voltage.set"
				args["voltage"] = 5.0
			}
			_, e := runWire(t, testdevice.Options{ReplyOverride: map[string]string{tc.cmd: tc.reply}}, key, args)
			if e == nil {
				t.Fatal("非法回复未拒绝")
			}
		})
	}
}
func TestTimeoutAndControlUnknown(t *testing.T) {
	sent, e := runWire(t, testdevice.Options{DropAfterControl: true}, "voltage.set", map[string]any{"voltage": 5.0})
	if e == nil || !sent {
		t.Fatal(sent, e)
	}
	mapped := transport.MapError(e, sent)
	if mapped == nil {
		t.Fatal("未映射控制未知结果")
	}
	_, e = runWire(t, testdevice.Options{Delay: 450 * time.Millisecond}, "settings.read", nil)
	if e == nil {
		t.Fatal("未超时")
	}
}
func TestModbusBadCRC(t *testing.T) {
	_, e := runWire(t, testdevice.Options{Protocol: "modbus", Corrupt: true}, "settings.read", nil)
	if e == nil {
		t.Fatal("损坏CRC被接受")
	}
}

func TestHistoricalReadOnlyDecoding(t *testing.T) {
	data, e := os.ReadFile("../../../testdata/history/longwei-readonly.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Transactions []struct {
			TX string `json:"tx"`
			RX string `json:"rx"`
		} `json:"transactions"`
	}
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Transactions) == 0 {
		t.Fatal("缺少历史夹具")
	}
	golden := fixture.Transactions[0]
	for _, key := range []string{"settings.read", "measurement.read", "output.read"} {
		t.Run(key, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			go func() {
				defer b.Close()
				p := make([]byte, 8)
				if _, e := io.ReadFull(b, p); e != nil {
					return
				}
				want, _ := hex.DecodeString(strings.ReplaceAll(golden.TX, " ", ""))
				if string(p) != string(want) {
					return
				}
				reply, _ := hex.DecodeString(strings.ReplaceAll(golden.RX, " ", ""))
				for _, v := range reply {
					if _, e := b.Write([]byte{v}); e != nil {
						return
					}
				}
			}()
			port := &testdevice.Port{Conn: a}
			_ = port.SetReadTimeout(50 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, e := Longwei(&transport.Wire{Context: ctx, Port: port}, transport.Config{Address: 1}, key, nil)
			if e != nil {
				t.Fatal(e)
			}
			switch key {
			case "settings.read":
				if r.Output["voltage"] != 12.0 || r.Output["current_limit"] != 5.0 {
					t.Fatal(r.Output)
				}
			case "measurement.read":
				if r.Output["voltage"] != 12.0 || r.Output["current"] != 0.0 {
					t.Fatal(r.Output)
				}
			case "output.read":
				if r.Output["enabled"] != true || r.Output["mode"] != "CV" {
					t.Fatal(r.Output)
				}
			}
		})
	}
}
func TestModbusExceptionAddressFunctionAndLength(t *testing.T) {
	for _, reply := range [][]byte{{1, 0x83, 2}, {2, 3, 2, 0, 1}, {1, 4, 2, 0, 1}, {1, 3, 2, 0, 1}} {
		t.Run(hex.EncodeToString(reply), func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			go func() {
				defer b.Close()
				req := make([]byte, 8)
				if _, e := io.ReadFull(b, req); e != nil {
					return
				}
				v := CRC(reply)
				_, _ = b.Write(append(append([]byte{}, reply...), byte(v), byte(v>>8)))
			}()
			p := &testdevice.Port{Conn: a}
			_ = p.SetReadTimeout(50 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, e := Modbus(&transport.Wire{Context: ctx, Port: p}, 1, 3, 0x1000, 6); e == nil {
				t.Fatal("Modbus异常回复未拒绝")
			}
		})
	}
}
