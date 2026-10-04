// Package secure 提供随机数、哈希与对称加密等安全原语。
//
// 所有随机数均来自 crypto/rand；所有比较敏感处使用时间无关比较。
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// crockford 是 Crockford Base32 字母表，去掉了容易读错/抄错的 I、L、O、U。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrDecrypt 表示密文无法解密（密钥变更、数据被篡改或格式错误）。
var ErrDecrypt = errors.New("密文解密失败")

// RandomHex 返回 n 字节随机数的十六进制字符串。
func RandomHex(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("随机字节数必须为正数")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// RandomCrockford 返回 n 个字符的 Crockford Base32 随机串。
//
// 256 能被 32 整除，因此对单字节取模不会引入偏置。
func RandomCrockford(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("随机长度必须为正数")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = crockford[int(b)%len(crockford)]
	}
	return string(out), nil
}

// RandomDigits 返回 n 位数字随机串，用于一次性码等场景。
func RandomDigits(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("随机长度必须为正数")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = byte('0' + int(b)%10)
	}
	return string(out), nil
}

// HashToken 返回 token 的 SHA-256 十六进制摘要。
// 数据库中只保存摘要，即使数据库副本泄露也无法直接冒用会话或一次性码。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual 在长度不等时也保持时间无关比较。
func ConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		subtle.ConstantTimeCompare([]byte(a), []byte(a))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// deriveKey 把任意长度的密钥材料拉伸成 AES-256 所需的 32 字节。
func deriveKey(key []byte) []byte {
	sum := sha256.Sum256(key)
	return sum[:]
}

// Encrypt 使用 AES-256-GCM 加密明文，返回 base64(nonce || ciphertext)。
// 每次调用都会生成新的随机 nonce。
func Encrypt(key []byte, plaintext string) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("加密密钥为空")
	}
	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 Encrypt 的产物；失败时返回 ErrDecrypt。
func Decrypt(key []byte, encoded string) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("解密密钥为空")
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: base64 解析失败", ErrDecrypt)
	}
	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("%w: 密文长度不足", ErrDecrypt)
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return string(plaintext), nil
}

// MaskTail 返回只保留末 4 位的掩码串，用于在界面/日志中展示敏感值。
func MaskTail(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return "****" + value[len(value)-4:]
}
