package buildinfo

import "testing"

func TestVersionOrderAndValidation(t *testing.T) {
	for _, v := range []string{"v1.2.3", "1.2", "01.2.3", "1.2.-3", "1.2.3-dev", "1.2.4294967296", "1.2.+3"} {
		if _, e := Parse(v); e == nil {
			t.Fatalf("非法版本被接受：%s", v)
		}
	}
	for _, pair := range [][2]string{{"0.1.9", "0.1.10"}, {"0.9.99", "0.10.0"}, {"1.99.99", "2.0.0"}} {
		if n, e := Compare(pair[0], pair[1]); e != nil || n != -1 {
			t.Fatal(pair, n, e)
		}
	}
	if n, e := Compare("0.1.0", "0.1.0"); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
