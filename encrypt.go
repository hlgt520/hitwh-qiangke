package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"math/rand"
)

// aesChars 与 encrypt.js 的 $aes_chars 一致（去掉了易混淆字符）。
const aesChars = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"

func randomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = aesChars[rand.Intn(len(aesChars))]
	}
	return string(b)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// encryptPassword 复现 encrypt.js 的密码加密：
// AES-CBC，key=盐值，iv=随机16字符，明文=随机64字符前缀+密码，输出 Base64。
func encryptPassword(password, salt string) string {
	if salt == "" {
		return password
	}
	key := []byte(salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return password
	}
	iv := []byte(randomString(16))
	data := []byte(randomString(64) + password)
	padded := pkcs7Pad(data, block.BlockSize())
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)
	return base64.StdEncoding.EncodeToString(ciphertext)
}
