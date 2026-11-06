package xiaomi

import "testing"

func TestLoginURLBoundary(t *testing.T) {
	for _, raw := range []string{
		"http://account.xiaomi.com/login", "https://account.xiaomi.com.evil.example/login",
		"https://xiaomi.com@127.0.0.1/login", "https://account.xiaomi.com:8443/login",
		"https://127.0.0.1/login", "https://account.xiaomi.com/login#fragment",
		"https://user:password@account.xiaomi.com/login", "//account.xiaomi.com/login",
	} {
		if validRemote(raw) {
			t.Fatalf("不受支持的登录地址通过：%s", raw)
		}
	}
	for _, raw := range []string{"https://account.xiaomi.com/pass/serviceLogin", "https://cn.account.xiaomi.com/login", "https://sts.api.mijia.tech/sts"} {
		if !validRemote(raw) {
			t.Fatalf("小米登录地址被拒绝：%s", raw)
		}
	}
}
