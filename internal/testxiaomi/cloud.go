// Package testxiaomi 是完全离线的米家协议模拟器，不导入生产接入模块的编解码。
package testxiaomi

import (
	"bytes"
	"compress/gzip"
	"crypto/rc4"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const UserID = "100001"
const SpecURN = "urn:miot-spec-v2:device:switch:0000A003:fixture-switch:1"
const UnsupportedURN = "urn:miot-spec-v2:device:sensor:0000A010:fixture-sensor:1"
const fixtureSecurity = "MDEyMzQ1Njc4OWFiY2RlZg=="

type login struct {
	ready chan struct{}
	uid   string
}

type Cloud struct {
	specs         map[string][]byte
	mu            sync.Mutex
	server        *httptest.Server
	logins        map[string]*login
	sequence      int
	state         bool
	writes        int
	reads         int
	requests      int
	expired       bool
	dropWrite     bool
	gate          chan struct{}
	entered       chan struct{}
	missingTime   bool
	propertyCode  int
	wrongProperty bool
	protocolError string
	blocked       sync.WaitGroup
}

func New() *Cloud {
	c := &Cloud{logins: map[string]*login{}}
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	return c
}

func (c *Cloud) SetSpec(urn string, raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.specs == nil {
		c.specs = map[string][]byte{}
	}
	c.specs[urn] = append([]byte(nil), raw...)
}

func (c *Cloud) Close() { c.server.Close() }

type localTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t localTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	copy.URL, copy.Host = &u, req.URL.Host
	res, err := t.base.RoundTrip(copy)
	if res != nil {
		res.Request = req
	}
	return res, err
}

// 外部 HTTPS 域名全部在 Transport 内映射到隔离的本地服务，不可能回落到真实云端。
func (c *Cloud) HTTPClient() *http.Client {
	u, _ := url.Parse(c.server.URL)
	return &http.Client{Transport: localTransport{base: http.DefaultTransport.(*http.Transport).Clone(), target: u}}
}

func (c *Cloud) Confirm(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.logins {
		select {
		case <-l.ready:
		default:
			l.uid = uid
			close(l.ready)
		}
	}
}

func (c *Cloud) Stats() (writes, reads, requests int, protocolError string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes, c.reads, c.requests, c.protocolError
}

func (c *Cloud) Block() (<-chan struct{}, func()) {
	c.mu.Lock()
	gate, entered := make(chan struct{}), make(chan struct{}, 10)
	c.gate, c.entered = gate, entered
	c.mu.Unlock()
	return entered, func() {
		c.mu.Lock()
		c.gate = nil
		c.entered = nil
		c.mu.Unlock()
		close(gate)
		c.blocked.Wait()
	}
}

func (c *Cloud) SetDropWrite(drop bool)      { c.mu.Lock(); c.dropWrite = drop; c.mu.Unlock() }
func (c *Cloud) SetExpired(expired bool)     { c.mu.Lock(); c.expired = expired; c.mu.Unlock() }
func (c *Cloud) SetMissingTime(missing bool) { c.mu.Lock(); c.missingTime = missing; c.mu.Unlock() }
func (c *Cloud) SetPropertyCode(code int)    { c.mu.Lock(); c.propertyCode = code; c.mu.Unlock() }
func (c *Cloud) SetWrongProperty(wrong bool) { c.mu.Lock(); c.wrongProperty = wrong; c.mu.Unlock() }

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func cookie(r *http.Request, name string) string {
	v, _ := r.Cookie(name)
	if v == nil {
		return ""
	}
	return v.Value
}

func (c *Cloud) fail(w http.ResponseWriter, message string) {
	c.mu.Lock()
	c.protocolError = message
	c.mu.Unlock()
	http.Error(w, "fixture protocol mismatch", 400)
}

func (c *Cloud) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host == "miot-spec.org" {
		if r.Header.Get("Cookie") != "" {
			c.fail(w, "规格请求泄漏了账号Cookie")
			return
		}
		urn := r.URL.Query().Get("type")
		c.mu.Lock()
		override := c.specs[urn]
		c.mu.Unlock()
		if override != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(override)
			return
		}
		services := []any{}
		if urn == SpecURN {
			for _, channel := range []int{2, 3} {
				services = append(services, map[string]any{"iid": channel, "description": "模拟开关", "properties": []any{
					map[string]any{"iid": 1, "type": "urn:miot-spec-v2:property:on:00000006", "format": "bool", "access": []string{"read", "write"}},
					map[string]any{"iid": 2, "type": "urn:miot-spec-v2:property:child-lock:00000010", "format": "bool", "access": []string{"read", "write"}},
				}})
			}
		}
		jsonResponse(w, map[string]any{"type": urn, "services": services})
		return
	}
	if r.Host == "sts.api.mijia.tech" {
		http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: "fixture-service", Domain: ".mijia.tech", Path: "/", Secure: true})
		io.WriteString(w, "ok")
		return
	}
	if r.Host == "account.xiaomi.com" {
		switch r.URL.Path {
		case "/pass/serviceLogin":
			if r.URL.Query().Get("sid") != "mijia" || cookie(r, "deviceId") == "" || r.UserAgent() == "" {
				c.fail(w, "登录服务或安装身份不匹配")
				return
			}
			if cookie(r, "passToken") == "fixture-pass" {
				c.mu.Lock()
				c.expired = false
				c.mu.Unlock()
				jsonResponse(w, map[string]any{"code": 0, "ssecurity": fixtureSecurity, "userId": UserID, "location": "https://sts.api.mijia.tech/sts"})
			} else {
				io.WriteString(w, "&&&START&&&")
				json.NewEncoder(w).Encode(map[string]any{"code": 70016, "location": "https://account.xiaomi.com/fe/service/login?sid=mijia&callback=https%3A%2F%2Fsts.api.mijia.tech%2Fsts"})
			}
		case "/longPolling/loginUrl":
			c.mu.Lock()
			c.sequence++
			id := strconv.Itoa(c.sequence)
			c.logins[id] = &login{ready: make(chan struct{})}
			c.mu.Unlock()
			jsonResponse(w, map[string]any{"code": 0, "loginUrl": "https://account.xiaomi.com/scan/" + id, "qr": "https://account.xiaomi.com/qr/" + id, "lp": "https://account.xiaomi.com/poll/" + id})
		default:
			if strings.HasPrefix(r.URL.Path, "/qr/") {
				// 明确为模拟图案，不代表真实账号登录二维码。
				img := image.NewRGBA(image.Rect(0, 0, 240, 240))
				for y := 0; y < 240; y++ {
					for x := 0; x < 240; x++ {
						v := uint8(255)
						if x > 20 && x < 220 && y > 20 && y < 220 && (x/12+y/12)%3 == 0 {
							v = 0
						}
						img.Set(x, y, color.RGBA{v, v, v, 255})
					}
				}
				w.Header().Set("Content-Type", "image/png")
				png.Encode(w, img)
			} else if strings.HasPrefix(r.URL.Path, "/poll/") {
				c.mu.Lock()
				l := c.logins[strings.TrimPrefix(r.URL.Path, "/poll/")]
				c.mu.Unlock()
				if l == nil {
					http.NotFound(w, r)
					return
				}
				select {
				case <-r.Context().Done():
					return
				case <-l.ready:
				}
				jsonResponse(w, map[string]any{"code": 0, "userId": l.uid, "cUserId": "fixture-cuser", "passToken": "fixture-pass", "ssecurity": fixtureSecurity, "location": "https://sts.api.mijia.tech/sts"})
			} else {
				c.fail(w, "不支持的登录路径")
			}
		}
		return
	}
	if r.Host != "api.mijia.tech" || r.Method != "POST" {
		c.fail(w, "非预期云端目标或方法")
		return
	}
	if cookie(r, "serviceToken") != "fixture-service" || cookie(r, "cUserId") != "fixture-cuser" {
		c.fail(w, "设备访问凭据缺失")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/app")
	data, key, err := decodeRequest(r, path)
	if err != nil {
		c.fail(w, err.Error())
		return
	}
	c.mu.Lock()
	c.requests++
	expired := c.expired
	gate, entered := c.gate, c.entered
	if gate != nil {
		c.blocked.Add(1)
		defer c.blocked.Done()
	}
	c.mu.Unlock()
	if expired {
		c.reply(w, key, map[string]any{"code": -3})
		return
	}
	var result any
	switch path {
	case "/v2/homeroom/gethome_merged":
		result = map[string]any{"homelist": []any{map[string]any{"id": "700", "uid": UserID, "name": "模拟实验室", "roomlist": []any{map[string]any{"name": "测试台", "dids": []string{"switch-1", "sensor-1", "switch-2"}}}}}}
	case "/home/home_device_list":
		var request map[string]any
		json.Unmarshal(data, &request)
		if request["home_id"] != float64(700) || request["home_owner"] != float64(100001) {
			c.fail(w, "家庭ID没有按数字发送")
			return
		}
		list := []any{map[string]any{"did": "switch-1", "name": "模拟双路开关", "model": "fixture.switch.v1", "spec_type": SpecURN, "isOnline": true, "token": "must-not-leak-device-token"}, map[string]any{"did": "sensor-1", "name": "模拟温湿度计", "model": "fixture.sensor.v1", "spec_type": UnsupportedURN, "isOnline": true}}
		more := true
		if request["start_did"] == "switch-1" {
			list = []any{map[string]any{"did": "switch-2", "name": "模拟第二台开关", "model": "fixture.switch.v1", "spec_type": SpecURN, "isOnline": true}}
			more = false
		}
		result = map[string]any{"device_info": list, "has_more": more, "max_did": "switch-1"}
	case "/v2/home/device_list_page":
		result = map[string]any{"list": []any{map[string]any{"did": "switch-1", "name": "重复共享设备", "model": "fixture.switch.v1", "spec_type": SpecURN, "owner": true}}}
	case "/miotspec/prop/get", "/miotspec/prop/set":
		var request struct {
			Params []map[string]any `json:"params"`
		}
		if json.Unmarshal(data, &request) != nil || len(request.Params) != 1 {
			c.fail(w, "属性请求格式错误")
			return
		}
		p := request.Params[0]
		if gate != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		c.mu.Lock()
		if path == "/miotspec/prop/set" {
			value, ok := p["value"].(bool)
			if !ok {
				c.mu.Unlock()
				c.fail(w, "开关值不是布尔值")
				return
			}
			c.state = value
			c.writes++
		} else {
			c.reads++
		}
		state, drop, missing, code, wrong := c.state, c.dropWrite, c.missingTime, c.propertyCode, c.wrongProperty
		c.mu.Unlock()
		if drop && path == "/miotspec/prop/set" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		v := map[string]any{"did": p["did"], "siid": p["siid"], "piid": p["piid"], "code": code}
		if path == "/miotspec/prop/get" {
			v["value"] = state
			if !missing {
				v["updateTime"] = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix()
			}
		}
		if wrong {
			v["did"] = "another-device"
		}
		result = []any{v}
	default:
		c.fail(w, "不支持的设备路径")
		return
	}
	c.reply(w, key, map[string]any{"code": 0, "result": result})
}

// 与生产代码独立实现的请求校验与响应编码。
func digest(path string, key []byte, fields map[string]string) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	s := "POST&" + path
	for _, name := range names {
		s += "&" + name + "=" + fields[name]
	}
	s += "&" + base64.StdEncoding.EncodeToString(key)
	h := sha1.Sum([]byte(s))
	return base64.StdEncoding.EncodeToString(h[:])
}

func transform(key, p []byte) []byte {
	c, _ := rc4.NewCipher(key)
	discard := make([]byte, 1024)
	c.XORKeyStream(discard, discard)
	out := make([]byte, len(p))
	c.XORKeyStream(out, p)
	return out
}

func decodeRequest(r *http.Request, path string) ([]byte, []byte, error) {
	if err := r.ParseForm(); err != nil {
		return nil, nil, fmt.Errorf("请求表单错误")
	}
	nonce, err := base64.StdEncoding.DecodeString(r.Form.Get("_nonce"))
	if err != nil || len(nonce) != 12 {
		return nil, nil, fmt.Errorf("nonce格式错误")
	}
	if r.Form.Get("ssecurity") != fixtureSecurity {
		return nil, nil, fmt.Errorf("签名密钥不匹配")
	}
	s, _ := base64.StdEncoding.DecodeString(fixtureSecurity)
	h := sha256.Sum256(append(s, nonce...))
	key := h[:]
	fields := map[string]string{"data": r.Form.Get("data"), "rc4_hash__": r.Form.Get("rc4_hash__")}
	if digest(path, key, fields) != r.Form.Get("signature") {
		return nil, nil, fmt.Errorf("密文签名不匹配")
	}
	b, err := base64.StdEncoding.DecodeString(fields["data"])
	if err != nil {
		return nil, nil, fmt.Errorf("密文格式错误")
	}
	plain := transform(key, b)
	hash, err := base64.StdEncoding.DecodeString(fields["rc4_hash__"])
	if err != nil || string(transform(key, hash)) != digest(path, key, map[string]string{"data": string(plain)}) {
		return nil, nil, fmt.Errorf("明文签名不匹配")
	}
	return plain, key, nil
}

func (c *Cloud) reply(w http.ResponseWriter, key []byte, v any) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	json.NewEncoder(gz).Encode(v)
	gz.Close()
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, base64.StdEncoding.EncodeToString(transform(key, b.Bytes())))
}
