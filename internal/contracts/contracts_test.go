package contracts

import "testing"

func TestDecimalAndBounds(t *testing.T) {
	s := Object(map[string]Schema{"v": Num(0, 30, .01, "V")}, "v")
	for _, v := range []float64{.29, .57, 1.01, 29.99} {
		if e := s.Validate(map[string]any{"v": v}); e != nil {
			t.Fatal(v, e)
		}
	}
	for _, p := range []map[string]any{{"v": .291}, {"v": 31.0}, {"v": 1.0, "extra": true}, {}} {
		if e := s.Validate(p); e == nil {
			t.Fatalf("非法输入被接受：%v", p)
		}
	}
	m, e := s.Parameters(map[string]any{}, map[string]any{"v": .29})
	if e != nil || m["v"] != .29 {
		t.Fatal(m, e)
	}
	if _, e = s.Tighten(map[string]Schema{"v": Num(0, 31, .01, "V")}); e == nil {
		t.Fatal("范围放宽未拒绝")
	}
	if _, e = s.Tighten(map[string]Schema{"v": Num(0, 20, .001, "V")}); e == nil {
		t.Fatal("精度放宽未拒绝")
	}
}
