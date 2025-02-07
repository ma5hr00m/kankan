package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
)

type Crypto struct {
	key    []byte
	block  cipher.Block
	nonce  []byte
}

func NewCrypto(password string) (*Crypto, error) {
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return &Crypto{
		key:    key[:],
		block:  block,
		nonce:  nonce,
	}, nil
}

func (c *Crypto) Encrypt(plaintext []byte) ([]byte, error) {
	aesgcm, err := cipher.NewGCM(c.block)
	if err != nil {
		return nil, err
	}

	return aesgcm.Seal(nil, c.nonce, plaintext, nil), nil
}

func (c *Crypto) Decrypt(ciphertext []byte) ([]byte, error) {
	aesgcm, err := cipher.NewGCM(c.block)
	if err != nil {
		return nil, err
	}

	return aesgcm.Open(nil, c.nonce, ciphertext, nil)
}
