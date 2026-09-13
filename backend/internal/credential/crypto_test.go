package credential

import (
	"bytes"
	"testing"
)

func TestCredentialEncryptionBindsOwnerKeyAndVersion(t *testing.T) {
	oldKey := bytes.Repeat([]byte{1}, 32)
	newKey := bytes.Repeat([]byte{2}, 32)
	oldRing, err := NewKeyringForTest(1, map[int][]byte{1: oldKey})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("sk-" + "not-stored-in-plaintext")
	encrypted, err := oldRing.Encrypt(42, 7, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Payload, plaintext) || encrypted.KeyVersion != 1 || len(encrypted.Nonce) != 12 {
		t.Fatalf("invalid encrypted value: %+v", encrypted)
	}

	reader, err := NewKeyringForTest(2, map[int][]byte{1: oldKey, 2: newKey})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reader.Decrypt(42, 7, encrypted)
	if err != nil || !bytes.Equal(actual, plaintext) || !reader.NeedsRotation(encrypted.KeyVersion) {
		t.Fatalf("decrypt = %q, %v", actual, err)
	}
	for _, pair := range [][2]int64{{43, 7}, {42, 8}} {
		if _, err := reader.Decrypt(pair[0], pair[1], encrypted); err == nil {
			t.Fatalf("changed AAD accepted: %+v", pair)
		}
	}
	tampered := encrypted
	tampered.Payload = append([]byte(nil), encrypted.Payload...)
	tampered.Payload[0] ^= 1
	if _, err := reader.Decrypt(42, 7, tampered); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}
