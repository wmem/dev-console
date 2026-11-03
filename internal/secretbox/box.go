// Package secretbox使用数据目录内的独立密钥保存可恢复凭据。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const KeyFile = "token.key"

type Box struct{ aead cipher.AEAD }

// Open只在尚无密文且没有密钥文件时初始化。已有密文不允许自动换密钥。
func Open(dir string, required bool) (*Box, error) {
	p := filepath.Join(dir, KeyFile)
	key, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		if required {
			return nil, errors.New("Token加密密钥token.key缺失，请恢复与数据库配套的完整数据备份")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = saveKey(dir, p, key); err != nil {
			clear(key)
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("无法读取Token加密密钥：%w", err)
	}
	defer clear(key)
	if len(key) != 32 {
		return nil, errors.New("Token加密密钥token.key损坏，请恢复配套备份，不能直接生成新密钥")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func saveKey(dir, target string, key []byte) error {
	f, err := os.CreateTemp(dir, ".token-key-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(key); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// 完整写入后才登记，不能覆盖另一实例已经创建的密钥。
	if err = os.Link(f.Name(), target); err != nil {
		return fmt.Errorf("初始化Token密钥失败，请检查数据目录或重试：%w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (b *Box) Seal(plaintext, aad string) string {
	return base64.RawStdEncoding.EncodeToString(b.aead.Seal(nil, nil, []byte(plaintext), []byte(aad)))
}
func (b *Box) Open(ciphertext, aad string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", errors.New("Token密文格式不正确")
	}
	raw, err := b.aead.Open(nil, nil, data, []byte(aad))
	if err != nil {
		return "", errors.New("Token解密失败，请检查数据库与token.key是否配套")
	}
	defer clear(raw)
	return string(raw), nil
}
