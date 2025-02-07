package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"hash"
	"sync"
)

type Crypto struct {
	block    cipher.Block
	hash     hash.Hash
	nonceLen int
	bufPool  sync.Pool
}

func NewCrypto(key string) (*Crypto, error) {
	if key == "" {
		return nil, errors.New("empty key")
	}

	h := sha256.New()
	h.Write([]byte(key))
	keyBytes := h.Sum(nil)

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}

	return &Crypto{
		block:    block,
		hash:     sha256.New(),
		nonceLen: 12,
		bufPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 4096)
			},
		},
	}, nil
}

func (c *Crypto) getBuf(size int) []byte {
	buf := c.bufPool.Get().([]byte)
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}

func (c *Crypto) putBuf(buf []byte) {
	c.bufPool.Put(buf[:0])
}

func (c *Crypto) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(c.block)
	if err != nil {
		return nil, err
	}

	ciphertext := c.getBuf(len(nonce) + len(plaintext) + aead.Overhead())
	defer c.putBuf(ciphertext)

	copy(ciphertext, nonce)

	encrypted := aead.Seal(ciphertext[c.nonceLen:c.nonceLen], nonce, plaintext, nil)
	result := make([]byte, len(nonce)+len(encrypted))
	copy(result, ciphertext[:len(nonce)+len(encrypted)])

	return result, nil
}

func (c *Crypto) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < c.nonceLen {
		return nil, errors.New("ciphertext too short")
	}

	aead, err := cipher.NewGCM(c.block)
	if err != nil {
		return nil, err
	}

	nonce := ciphertext[:c.nonceLen]
	encrypted := ciphertext[c.nonceLen:]

	plaintext := c.getBuf(len(encrypted) - aead.Overhead())
	defer c.putBuf(plaintext)

	decrypted, err := aead.Open(plaintext[:0], nonce, encrypted, nil)
	if err != nil {
		return nil, err
	}

	result := make([]byte, len(decrypted))
	copy(result, decrypted)

	return result, nil
}

func (c *Crypto) Hash(data []byte) []byte {
	c.hash.Reset()
	c.hash.Write(data)
	return c.hash.Sum(nil)
}
