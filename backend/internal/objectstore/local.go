package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,511}$`)

type Metadata struct {
	Size int64
}

type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("object root must be absolute")
	}
	clean := filepath.Clean(root)
	if clean == string(filepath.Separator) {
		return nil, errors.New("object root must not be the filesystem root")
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, fmt.Errorf("create object root: %w", err)
	}
	return &Local{root: clean}, nil
}

func (store *Local) Put(ctx context.Context, key string, body io.Reader, size int64, expectedSHA256 string) error {
	path, err := store.resolve(key)
	if err != nil {
		return err
	}
	if size <= 0 || len(expectedSHA256) != 64 {
		return errors.New("object size and SHA-256 are required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create object directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".canvas-object-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary object: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary object: %w", err)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), &contextReader{ctx: ctx, reader: io.LimitReader(body, size+1)})
	if err != nil {
		return fmt.Errorf("write object: %w", err)
	}
	if written != size {
		return errors.New("object byte count mismatch")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return errors.New("object SHA-256 mismatch")
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync object: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close object: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install object: %w", err)
	}
	committed = true
	return nil
}

func (store *Local) Open(_ context.Context, key string) (io.ReadCloser, Metadata, error) {
	path, err := store.resolve(key)
	if err != nil {
		return nil, Metadata{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("open object: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, Metadata{}, fmt.Errorf("stat object: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, Metadata{}, errors.New("object is not a regular file")
	}
	return file, Metadata{Size: info.Size()}, nil
}

func (store *Local) Delete(_ context.Context, key string) error {
	path, err := store.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (store *Local) Probe(ctx context.Context) error {
	payload := []byte("canvas-object-store-readiness")
	digest := sha256.Sum256(payload)
	key := "health/readiness"
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload)), hex.EncodeToString(digest[:])); err != nil {
		return err
	}
	reader, metadata, err := store.Open(ctx, key)
	if err != nil {
		return err
	}
	readPayload, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	deleteErr := store.Delete(ctx, key)
	if readErr != nil || closeErr != nil || deleteErr != nil || metadata.Size != int64(len(payload)) || string(readPayload) != string(payload) {
		return errors.New("object-store readiness round trip failed")
	}
	return nil
}

func (store *Local) resolve(key string) (string, error) {
	if !keyPattern.MatchString(key) || strings.Contains(key, "//") {
		return "", errors.New("invalid object key")
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid object key")
	}
	path := filepath.Join(store.root, clean)
	relative, err := filepath.Rel(store.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("object key escapes the root")
	}
	return path, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, reader.ctx.Err()
	default:
		return reader.reader.Read(buffer)
	}
}
