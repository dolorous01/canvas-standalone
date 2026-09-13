package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

type Ciphertext struct {
	Payload    []byte
	Nonce      []byte
	KeyVersion int
}

type Keyring struct {
	active int
	keys   map[int][]byte
	random io.Reader
}

func LoadKeyring(active int, paths map[int]string) (*Keyring, error) {
	keys := make(map[int][]byte, len(paths))
	for version, path := range paths {
		encoded, err := secretfile.Read(path, 256)
		if err != nil {
			return nil, fmt.Errorf("read credential key v%d: %w", version, err)
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(string(encoded))
		secretfile.Zero(encoded)
		if err != nil || len(decoded) != 32 {
			secretfile.Zero(decoded)
			return nil, fmt.Errorf("credential key v%d must be standard base64 for exactly 32 bytes", version)
		}
		keys[version] = decoded
	}
	if active > 0 {
		if _, ok := keys[active]; !ok {
			return nil, errors.New("active credential key is unavailable")
		}
	}
	return &Keyring{active: active, keys: keys, random: rand.Reader}, nil
}

func NewKeyringForTest(active int, keys map[int][]byte) (*Keyring, error) {
	copyKeys := make(map[int][]byte, len(keys))
	for version, key := range keys {
		if len(key) != 32 {
			return nil, errors.New("AES-256-GCM keys must be 32 bytes")
		}
		copyKeys[version] = append([]byte(nil), key...)
	}
	if _, ok := copyKeys[active]; !ok {
		return nil, errors.New("active credential key is unavailable")
	}
	return &Keyring{active: active, keys: copyKeys, random: rand.Reader}, nil
}

func (ring *Keyring) Encrypt(ownerID, externalKeyID int64, plaintext []byte) (Ciphertext, error) {
	if ring == nil || ring.active <= 0 || len(plaintext) == 0 {
		return Ciphertext{}, errors.New("credential encryption is unavailable")
	}
	gcm, err := ring.gcm(ring.active)
	if err != nil {
		return Ciphertext{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(ring.random, nonce); err != nil {
		return Ciphertext{}, errors.New("generate credential nonce")
	}
	payload := gcm.Seal(nil, nonce, plaintext, additionalData(ownerID, externalKeyID))
	return Ciphertext{Payload: payload, Nonce: nonce, KeyVersion: ring.active}, nil
}

func (ring *Keyring) Decrypt(ownerID, externalKeyID int64, encrypted Ciphertext) ([]byte, error) {
	if ring == nil || ownerID <= 0 || externalKeyID <= 0 {
		return nil, errors.New("credential decryption is unavailable")
	}
	gcm, err := ring.gcm(encrypted.KeyVersion)
	if err != nil {
		return nil, err
	}
	if len(encrypted.Nonce) != gcm.NonceSize() {
		return nil, errors.New("credential nonce is invalid")
	}
	plaintext, err := gcm.Open(nil, encrypted.Nonce, encrypted.Payload, additionalData(ownerID, externalKeyID))
	if err != nil {
		return nil, errors.New("credential authentication failed")
	}
	return plaintext, nil
}

func (ring *Keyring) NeedsRotation(version int) bool {
	return ring != nil && ring.active > 0 && version != ring.active
}

func (ring *Keyring) Close() {
	if ring == nil {
		return
	}
	for version, key := range ring.keys {
		secretfile.Zero(key)
		delete(ring.keys, version)
	}
}

func (ring *Keyring) gcm(version int) (cipher.AEAD, error) {
	key, ok := ring.keys[version]
	if !ok {
		return nil, fmt.Errorf("credential key version %d is unavailable", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize credential cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize credential GCM")
	}
	return gcm, nil
}

func additionalData(ownerID, externalKeyID int64) []byte {
	return []byte("canvas-credential:v1:" + strconv.FormatInt(ownerID, 10) + ":" + strconv.FormatInt(externalKeyID, 10))
}
