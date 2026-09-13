package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestLocalRoundTripAndTraversalProtection(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := "object-payload"
	digest := sha256.Sum256([]byte(payload))
	if err := store.Put(context.Background(), "users/42/assets/a1", strings.NewReader(payload), int64(len(payload)), hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	reader, metadata, err := store.Open(context.Background(), "users/42/assets/a1")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	actual, err := io.ReadAll(reader)
	if err != nil || string(actual) != payload || metadata.Size != int64(len(payload)) {
		t.Fatalf("round trip = %q, %+v, %v", actual, metadata, err)
	}
	for _, key := range []string{"../secret", "/absolute", "a//b", "a/../../secret"} {
		if _, _, err := store.Open(context.Background(), key); err == nil {
			t.Fatalf("unsafe object key was accepted: %q", key)
		}
	}
}

func TestLocalRejectsSizeAndDigestMismatchAndProbes(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := "payload"
	digest := sha256.Sum256([]byte(payload))
	if err := store.Put(context.Background(), "size", strings.NewReader(payload), 2, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("size mismatch was accepted")
	}
	if err := store.Put(context.Background(), "hash", strings.NewReader(payload), int64(len(payload)), strings.Repeat("0", 64)); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if err := store.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
}
