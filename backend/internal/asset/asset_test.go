package asset

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeFileNamePreservesUTF8Boundary(t *testing.T) {
	name := strings.Repeat("图", 100) + ".png"
	result := safeFileName(name)
	if len(result) > 240 {
		t.Fatalf("filename is too long: %d", len(result))
	}
	if !utf8.ValidString(result) {
		t.Fatalf("filename is not valid UTF-8: %q", result)
	}
}

func TestInspectValidatesImageSignatureAndDimensions(t *testing.T) {
	pngPayload, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	kind, mimeType, width, height, err := inspect(pngPayload, "image/png", 0, 0, nil)
	if err != nil || kind != "image" || mimeType != "image/png" || width != 1 || height != 1 {
		t.Fatalf("inspect() = %q, %q, %d, %d, %v", kind, mimeType, width, height, err)
	}
	if _, _, _, _, err := inspect(pngPayload, "image/jpeg", 0, 0, nil); !errors.Is(err, ErrInvalidFile) {
		t.Fatal("declared MIME mismatch was accepted")
	}
}

func TestInspectRejectsUnknownAndIncompleteMediaMetadata(t *testing.T) {
	if _, _, _, _, err := inspect([]byte("plain text"), "text/plain", 0, 0, nil); !errors.Is(err, ErrInvalidFile) {
		t.Fatal("text upload was accepted")
	}
	mp4 := append([]byte{0, 0, 0, 20}, []byte("ftypisom0000")...)
	if _, _, _, _, err := inspect(mp4, "video/mp4", 1920, 1080, nil); !errors.Is(err, ErrInvalidFile) {
		t.Fatal("video without duration was accepted")
	}
}
