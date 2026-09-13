package secretfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const DefaultMaxSize = int64(16 << 10)

func Read(path string, maxSize int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("secret file path must be absolute")
	}
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect secret file: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("secret file must not be a symbolic link")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret file must be regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secret file must not be accessible by group or others")
	}
	if info.Size() <= 0 || info.Size() > maxSize {
		return nil, errors.New("secret file size is invalid")
	}
	payload := make([]byte, info.Size())
	read, err := file.Read(payload)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	if int64(read) != info.Size() {
		return nil, errors.New("secret file changed while reading")
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return nil, errors.New("secret file is empty")
	}
	return payload, nil
}

func Zero(payload []byte) {
	for index := range payload {
		payload[index] = 0
	}
}
