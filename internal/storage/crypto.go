// Package storage is sys1's pooled storage: encrypted, content-addressed
// chunks, the on-disk store machines keep them in, and snapshots that turn
// a folder into chunks and back.
//
// Every chunk is sealed with AES-256-GCM before it leaves the machine that
// made it. A chunk's ID is the SHA-256 of its sealed bytes, so anyone
// holding a chunk can prove it is intact without being able to read it.
// See docs/STORAGE.md.
package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
)

// ChunkSize is the largest plaintext a chunk carries.
const ChunkSize = 4 << 20

// MaxSealed is the largest a sealed chunk can be.
const MaxSealed = ChunkSize + 12 + 16

const keySize = 32

var (
	ErrCorrupt = errors.New("storage: chunk is corrupt or was tampered with")
	ErrKey     = errors.New("storage: chunk cannot be opened with this key")
	idRe       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidID reports whether id looks like a chunk ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

// NewPoolKey makes the node's root key. Every data key derives from it.
func NewPoolKey() []byte {
	k := make([]byte, keySize)
	rand.Read(k)
	return k
}

// DeriveKey makes the key for one owner of data, such as "task:<id>" or
// "backup:<id>". The node hands a derived key only to the machine that
// produces or restores that owner's data; machines that merely hold its
// chunks never get it.
func DeriveKey(poolKey []byte, owner string) []byte {
	k, err := hkdf.Key(sha256.New, poolKey, nil, "sys1 data key: "+owner, keySize)
	if err != nil {
		panic(err) // only fails for impossible lengths
	}
	return k
}

// ID is the chunk ID of sealed bytes.
func ID(sealed []byte) string {
	sum := sha256.Sum256(sealed)
	return hex.EncodeToString(sum[:])
}

// Seal encrypts plaintext with key and returns the sealed chunk and its ID.
// The layout is nonce (12 bytes) followed by the GCM ciphertext and tag.
func Seal(key, plaintext []byte) (sealed []byte, id string, err error) {
	if len(plaintext) > ChunkSize {
		return nil, "", errors.New("storage: chunk too large")
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, "", err
	}
	sealed = make([]byte, 12, 12+len(plaintext)+aead.Overhead())
	rand.Read(sealed)
	sealed = aead.Seal(sealed, sealed[:12], plaintext, nil)
	return sealed, ID(sealed), nil
}

// Open checks the sealed chunk against its ID, then decrypts it.
func Open(key, sealed []byte, id string) ([]byte, error) {
	if ID(sealed) != id {
		return nil, ErrCorrupt
	}
	if len(sealed) < 12+16 {
		return nil, ErrCorrupt
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		return nil, ErrKey
	}
	return out, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, errors.New("storage: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
