package auth

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"devconsole/internal/contracts"
)

// 密文不属于Token公开结构，列表和文档不会意外序列化凭据。
type encryptedToken struct {
	Version int    `json:"version"`
	Payload string `json:"payload"`
}

func tokenAAD(t Token) string { return "dconsole-token:v1\x00" + t.ProjectID + "\x00" + t.ID }
func publicToken(t Token, recoverable bool) TokenMetadata {
	t.Digest = ""
	return TokenMetadata{Token: t, Recoverable: recoverable}
}

func (a *Auth) activeToken(project, id string) (Token, error) {
	var t Token
	err := a.Store.Get("access_tokens", id, &t)
	if errors.Is(err, sql.ErrNoRows) {
		return t, contracts.Fail("SERVICE_NOT_FOUND", "Token不存在")
	}
	if err != nil {
		return t, err
	}
	if t.ProjectID != project {
		return Token{}, contracts.Fail("SERVICE_NOT_FOUND", "Token不存在")
	}
	if !t.Enabled || (t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now())) {
		return Token{}, contracts.Fail("CONFIG_CONFLICT", "Token已撤销或过期，不能复制原文")
	}
	return t, nil
}

func (a *Auth) RevealToken(project, id string) (TokenMetadata, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.activeToken(project, id)
	if err != nil {
		return TokenMetadata{}, "", err
	}
	var secret encryptedToken
	if err = a.Store.Get("token_secrets", id, &secret); errors.Is(err, sql.ErrNoRows) {
		return TokenMetadata{}, "", contracts.Fail("CONFIG_CONFLICT", "旧Token只保存了摘要，请补存已保存的原文，或新建Token")
	}
	if err != nil {
		return TokenMetadata{}, "", err
	}
	if secret.Version != 1 || a.Store.Secrets == nil {
		return TokenMetadata{}, "", contracts.Fail("INTERNAL_ERROR", "Token加密格式不受支持")
	}
	raw, err := a.Store.Secrets.Open(secret.Payload, tokenAAD(t))
	if err != nil {
		return TokenMetadata{}, "", contracts.Fail("INTERNAL_ERROR", err.Error())
	}
	if !Equal(Digest(raw), t.Digest) {
		return TokenMetadata{}, "", contracts.Fail("INTERNAL_ERROR", "Token密文与鉴权记录不匹配")
	}
	return publicToken(t, true), raw, nil
}

// ImportTokenSecret只为旧记录补存已知原文，不修改Token值、身份、作用域或有效期。
func (a *Auth) ImportTokenSecret(project, id, raw string) (TokenMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.activeToken(project, id)
	if err != nil {
		return TokenMetadata{}, err
	}
	raw = strings.TrimSpace(raw)
	if !Equal(Digest(raw), t.Digest) {
		return TokenMetadata{}, contracts.Fail("INVALID_ARGUMENT", "原文与这个Token不匹配，未修改原Token")
	}
	if a.Store.Secrets == nil {
		return TokenMetadata{}, contracts.Fail("INTERNAL_ERROR", "Token加密不可用")
	}
	var existing encryptedToken
	err = a.Store.Get("token_secrets", id, &existing)
	if err == nil {
		return TokenMetadata{}, contracts.Fail("CONFIG_CONFLICT", "此Token已经加密保存，可直接复制")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TokenMetadata{}, err
	}
	secret := encryptedToken{Version: 1, Payload: a.Store.Secrets.Seal(raw, tokenAAD(t))}
	if err = a.Store.Put("token_secrets", id, secret); err != nil {
		return TokenMetadata{}, err
	}
	return publicToken(t, true), nil
}
