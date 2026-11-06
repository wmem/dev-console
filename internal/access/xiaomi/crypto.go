package xiaomi

import (
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// 米家 App 的 RC4-drop1024 与签名格式是协议兼容要求；传输仍使用 HTTPS。
// 协议依据及许可来源见 protocol.md；这里不依赖社区客户端的运行时或源码。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("无法生成安全随机数")
	}
	return hex.EncodeToString(b)
}

func newNonce() ([]byte, error) {
	n := make([]byte, 12)
	if _, err := rand.Read(n[:8]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(n[8:], uint32(time.Now().Unix()/60))
	return n, nil
}

func nonceKey(secret string, nonce []byte) ([]byte, error) {
	s, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(s) < 16 {
		return nil, fmt.Errorf("米家会话签名密钥无效")
	}
	h := sha256.New()
	h.Write(s)
	h.Write(nonce)
	return h.Sum(nil), nil
}

func cryptRC4(key, data []byte) ([]byte, error) {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	skip := make([]byte, 1024)
	cipher.XORKeyStream(skip, skip)
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out, nil
}

func signature(path string, key []byte, fields url.Values) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{"POST", path}
	for _, k := range keys {
		parts = append(parts, k+"="+fields.Get(k))
	}
	parts = append(parts, base64.StdEncoding.EncodeToString(key))
	digest := sha1.Sum([]byte(strings.Join(parts, "&")))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func encryptedForm(path, secret string, nonce, data []byte) (url.Values, error) {
	key, err := nonceKey(secret, nonce)
	if err != nil {
		return nil, err
	}
	fields := url.Values{"data": {string(data)}}
	fields.Set("rc4_hash__", signature(path, key, fields))
	for _, name := range []string{"data", "rc4_hash__"} {
		v, err := cryptRC4(key, []byte(fields.Get(name)))
		if err != nil {
			return nil, err
		}
		fields.Set(name, base64.StdEncoding.EncodeToString(v))
	}
	fields.Set("signature", signature(path, key, fields))
	fields.Set("ssecurity", secret)
	fields.Set("_nonce", base64.StdEncoding.EncodeToString(nonce))
	return fields, nil
}
