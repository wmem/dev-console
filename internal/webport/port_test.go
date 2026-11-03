package webport

import "testing"

func TestBrowserHTTPPorts(t *testing.T) {
	for _, port := range []int{-1, 0, 1, 21, 22, 25, 53, 6000, 6667, 10080, 65536} {
		if err := Validate(port); err == nil {
			t.Fatal("浏览器不能使用的端口被接受", port)
		}
	}
	for _, port := range []int{80, 443, 8080, 8081, 9000, 10079, 10081, 65535} {
		if err := Validate(port); err != nil {
			t.Fatal(port, err)
		}
	}
}
