package xiaomi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	c "devconsole/internal/contracts"
)

const maxResponse = 4 << 20

// Credentials 只在接入模块和加密存储之间传递，不能作为 HTTP 响应。
type Credentials struct {
	UserID       string    `json:"user_id"`
	CUserID      string    `json:"c_user_id"`
	ServiceToken string    `json:"service_token"`
	PassToken    string    `json:"pass_token"`
	SSecurity    string    `json:"ssecurity"`
	DeviceID     string    `json:"device_id"`
	PassO        string    `json:"pass_o"`
	UserAgent    string    `json:"user_agent"`
	RefreshedAt  time.Time `json:"refreshed_at"`
}

type Client struct {
	HTTP       *http.Client
	AccountURL string
	APIURL     string
	SpecURL    string
}

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 二维码长轮询可能两分钟后才返回响应头；各操作由调用上下文限定总时长。
	transport.ResponseHeaderTimeout = 0
	return &Client{
		HTTP:       &http.Client{Transport: transport},
		AccountURL: "https://account.xiaomi.com", APIURL: "https://api.mijia.tech/app",
		SpecURL: "https://miot-spec.org/miot-spec-v2/instance",
	}
}

// 所有远端错误只返回经过选择的固定信息；不反射 Cookie、URL、响应正文或签名参数。
type cloudError struct {
	code int
	auth bool
}

func (e *cloudError) Error() string {
	return fmt.Sprintf("米家云接口拒绝请求（代码 %d）", e.code)
}
func authFailure(err error) bool {
	var e *cloudError
	return errors.As(err, &e) && e.auth
}

func boundedRead(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, c.Fail("PROTOCOL_ERROR", "米家响应读取失败或超过大小限制")
	}
	return b, nil
}

func validRemote(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, domain := range []string{"xiaomi.com", "mi.com", "mijia.tech", "mi-img.com"} {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return true
		}
	}
	return false
}

// URL 验证位于每次请求及重定向之前。测试通过自定义 Transport 回放 HTTPS 请求，
// 生产配置不提供修改远端地址或放宽证书/域名检查的入口。
func (cl *Client) session(jar http.CookieJar, redirects bool) *http.Client {
	h := *cl.HTTP
	h.Jar = jar
	h.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !redirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 || !validRemote(req.URL.String()) {
			return c.Fail("PROTOCOL_ERROR", "米家登录跳转地址不受支持")
		}
		return nil
	}
	return &h
}

func (cl *Client) get(ctx context.Context, h *http.Client, raw string, cred Credentials) ([]byte, *http.Response, error) {
	if !validRemote(raw) {
		return nil, nil, c.Fail("PROTOCOL_ERROR", "米家登录地址不受支持")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, nil, c.Fail("PROTOCOL_ERROR", "米家登录地址无效")
	}
	req.Header.Set("User-Agent", cred.UserAgent)
	res, err := h.Do(req)
	if err != nil {
		return nil, nil, c.Fail("DEVICE_OFFLINE", "无法连接米家登录服务，请检查网络后重新扫码")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res, &cloudError{code: res.StatusCode, auth: res.StatusCode == 401 || res.StatusCode == 403}
	}
	b, err := boundedRead(res.Body, maxResponse)
	return b, res, err
}

type remoteID string

func (v *remoteID) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
		if s == "null" {
			s = ""
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return fmt.Errorf("标识符格式无效")
			}
		}
	}
	*v = remoteID(s)
	return nil
}

type loginReply struct {
	Code      int      `json:"code"`
	Location  string   `json:"location"`
	QR        string   `json:"qr"`
	LoginURL  string   `json:"loginUrl"`
	PollURL   string   `json:"lp"`
	UserID    remoteID `json:"userId"`
	CUserID   string   `json:"cUserId"`
	PassToken string   `json:"passToken"`
	SSecurity string   `json:"ssecurity"`
}

func parseLogin(b []byte) (loginReply, error) {
	var out loginReply
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("&&&START&&&")))
	if err := json.Unmarshal(b, &out); err != nil {
		return out, c.Fail("PROTOCOL_ERROR", "米家登录响应格式无效")
	}
	return out, nil
}

type qrLogin struct {
	credentials Credentials
	client      *http.Client
	pollURL     string
	image       []byte
	imageType   string
}

func identity() Credentials {
	device, passO := randomHex(8), randomHex(8)
	// 固定安装身份随凭据持久化，避免每次请求表现为一台新手机。
	installation := strings.ToUpper(randomHex(20))
	agent := "Android-15-11.0.701-Xiaomi-23046RP50C-OS2.0.212.0.VMYCNXM-" + installation + "-CN-" + strings.ToUpper(randomHex(16)) + "-" + strings.ToUpper(randomHex(16)) + "-SmartHome-MI_APP_STORE-" + installation + "|" + strings.ToUpper(randomHex(20)) + "|" + passO + "-64"
	return Credentials{DeviceID: device, PassO: passO, UserAgent: agent}
}

func (cl *Client) loginClient(cred Credentials, refresh bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(cl.AccountURL)
	cookies := []*http.Cookie{{Name: "deviceId", Value: cred.DeviceID}, {Name: "pass_o", Value: cred.PassO}, {Name: "uLocale", Value: "zh_CN"}}
	if refresh {
		cookies = append(cookies, &http.Cookie{Name: "passToken", Value: cred.PassToken}, &http.Cookie{Name: "userId", Value: cred.UserID}, &http.Cookie{Name: "cUserId", Value: cred.CUserID})
	}
	jar.SetCookies(u, cookies)
	return cl.session(jar, true)
}

func (cl *Client) loginLocation(ctx context.Context, h *http.Client, cred Credentials) (loginReply, error) {
	b, _, err := cl.get(ctx, h, cl.AccountURL+"/pass/serviceLogin?"+url.Values{"sid": {"mijia"}, "_json": {"true"}, "_locale": {"zh_CN"}}.Encode(), cred)
	if err != nil {
		return loginReply{}, err
	}
	return parseLogin(b)
}

func (cl *Client) beginLogin(ctx context.Context, cred Credentials) (*qrLogin, error) {
	h := cl.loginClient(cred, false)
	location, err := cl.loginLocation(ctx, h, cred)
	if err != nil {
		return nil, err
	}
	if !validRemote(location.Location) {
		return nil, c.Fail("PROTOCOL_ERROR", "米家没有返回有效扫码登录入口")
	}
	u, _ := url.Parse(location.Location)
	params := u.Query()
	params.Set("_qrsize", "240")
	params.Set("_hasLogo", "false")
	params.Set("_dc", fmt.Sprint(time.Now().UnixMilli()))
	b, _, err := cl.get(ctx, h, cl.AccountURL+"/longPolling/loginUrl?"+params.Encode(), cred)
	if err != nil {
		return nil, err
	}
	qr, err := parseLogin(b)
	if err != nil {
		return nil, err
	}
	if qr.Code != 0 || !validRemote(qr.PollURL) || !validRemote(qr.QR) {
		return nil, c.Fail("PROTOCOL_ERROR", "米家没有返回有效二维码，请稍后重新登录")
	}
	img, _, err := cl.get(ctx, h, qr.QR, cred)
	if err != nil {
		return nil, err
	}
	typ := http.DetectContentType(img)
	if len(img) > 1<<20 || (typ != "image/png" && typ != "image/jpeg") {
		return nil, c.Fail("PROTOCOL_ERROR", "米家二维码图片格式不受支持")
	}
	return &qrLogin{credentials: cred, client: h, pollURL: qr.PollURL, image: img, imageType: typ}, nil
}

func (cl *Client) completeLogin(ctx context.Context, q *qrLogin) (Credentials, error) {
	b, _, err := cl.get(ctx, q.client, q.pollURL, q.credentials)
	if err != nil {
		return Credentials{}, err
	}
	v, err := parseLogin(b)
	if err != nil {
		return Credentials{}, err
	}
	if v.Code != 0 || v.UserID == "" || v.SSecurity == "" || v.PassToken == "" || v.CUserID == "" {
		return Credentials{}, c.Fail("DEVICE_OFFLINE", "扫码未完成或需要在手机确认，请重新扫码")
	}
	cred := q.credentials
	cred.UserID, cred.CUserID, cred.PassToken, cred.SSecurity = string(v.UserID), v.CUserID, v.PassToken, v.SSecurity
	return cl.finishLogin(ctx, q.client, cred, v.Location)
}

func (cl *Client) finishLogin(ctx context.Context, h *http.Client, cred Credentials, location string) (Credentials, error) {
	_, res, err := cl.get(ctx, h, location, cred)
	if err != nil {
		return Credentials{}, err
	}
	for _, raw := range []string{location, res.Request.URL.String(), cl.APIURL} {
		u, _ := url.Parse(raw)
		for _, cookie := range append(h.Jar.Cookies(u), res.Cookies()...) {
			if cookie.Name == "serviceToken" && cookie.Value != "" {
				cred.ServiceToken = cookie.Value
			}
		}
	}
	if cred.ServiceToken == "" {
		return Credentials{}, c.Fail("PROTOCOL_ERROR", "米家未返回设备访问凭据，请重新扫码")
	}
	if _, err := nonceKey(cred.SSecurity, make([]byte, 12)); err != nil {
		return Credentials{}, c.Fail("PROTOCOL_ERROR", "米家未返回有效签名凭据")
	}
	cred.RefreshedAt = time.Now().UTC()
	return cred, nil
}

func (cl *Client) refresh(ctx context.Context, cred Credentials) (Credentials, error) {
	h := cl.loginClient(cred, true)
	v, err := cl.loginLocation(ctx, h, cred)
	if err != nil {
		return Credentials{}, err
	}
	if v.Code != 0 || v.SSecurity == "" || (v.UserID != "" && string(v.UserID) != cred.UserID) {
		return Credentials{}, c.Fail("DEVICE_OFFLINE", "小米登录态已失效，请重新扫码")
	}
	cred.SSecurity = v.SSecurity
	if v.PassToken != "" {
		cred.PassToken = v.PassToken
	}
	cred.ServiceToken = ""
	return cl.finishLogin(ctx, h, cred, v.Location)
}

func (cl *Client) request(ctx context.Context, cred Credentials, path string, data any, out any, write bool) error {
	if err := ctx.Err(); err != nil {
		return c.Fail("TIMEOUT", "米家请求尚未发送，调用已取消或超时")
	}
	body, err := json.Marshal(data)
	if err != nil {
		return c.Fail("INVALID_ARGUMENT", "米家参数不能序列化")
	}
	nonce, err := newNonce()
	if err != nil {
		return c.Fail("INTERNAL_ERROR", "无法生成米家请求随机数")
	}
	form, err := encryptedForm(path, cred.SSecurity, nonce, body)
	if err != nil {
		return c.Fail("DEVICE_OFFLINE", "米家会话无效，请重新扫码")
	}
	var attempted atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { attempted.Store(true) }}
	ctx = httptrace.WithClientTrace(ctx, trace)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.APIURL+path, strings.NewReader(form.Encode()))
	if err != nil || !validRemote(cl.APIURL+path) {
		return c.Fail("INTERNAL_ERROR", "米家接口地址无效")
	}
	req.GetBody = nil // 控制请求不可自动重放，也不跟随 HTTP 重定向。
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", cred.UserAgent)
	req.Header.Set("miot-encrypt-algorithm", "ENCRYPT-RC4")
	req.Header.Set("miot-accept-encoding", "GZIP")
	req.Header.Set("x-xiaomi-protocal-flag-cli", "PROTOCAL-HTTP2")
	for name, value := range map[string]string{"cUserId": cred.CUserID, "serviceToken": cred.ServiceToken, "yetAnotherServiceToken": cred.ServiceToken, "PassportDeviceId": cred.DeviceID, "locale": "zh_CN", "countryCode": "CN", "timezone_id": "Asia/Shanghai", "timezone": "GMT+08:00", "channel": "MI_APP_STORE"} {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	res, err := cl.session(nil, false).Do(req)
	uncertain := func() error {
		if write && attempted.Load() {
			return &c.Error{Code: "OUTCOME_UNKNOWN", Message: "米家控制请求可能已发出，无法确认结果；未自动重试，请显式读取状态", Outcome: "unknown"}
		}
		if ctx.Err() != nil {
			return c.Fail("TIMEOUT", "米家请求已取消或超时")
		}
		return c.Fail("DEVICE_OFFLINE", "米家接口暂时不可达")
	}
	if err != nil {
		return uncertain()
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return &cloudError{code: res.StatusCode, auth: true}
	}
	if res.StatusCode != http.StatusOK {
		return uncertain()
	}
	b, err := boundedRead(res.Body, maxResponse)
	if err != nil {
		return uncertain()
	}
	if !json.Valid(b) {
		encrypted, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			return uncertain()
		}
		key, _ := nonceKey(cred.SSecurity, nonce)
		b, err = cryptRC4(key, encrypted)
		if err != nil {
			return uncertain()
		}
		if bytes.HasPrefix(b, []byte{0x1f, 0x8b}) {
			gz, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				return uncertain()
			}
			b, err = boundedRead(gz, maxResponse)
			gz.Close()
			if err != nil {
				return uncertain()
			}
		}
	}
	var envelope struct {
		Code   *int            `json:"code"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &envelope) != nil || envelope.Code == nil {
		return uncertain()
	}
	if *envelope.Code != 0 {
		n := *envelope.Code
		return &cloudError{code: n, auth: n == -3 || n == -10001 || n == 401 || n == 403 || n == 70016}
	}
	if len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) || json.Unmarshal(envelope.Result, out) != nil {
		return uncertain()
	}
	return nil
}
