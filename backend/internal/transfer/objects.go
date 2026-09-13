package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func CopyObjects(ctx context.Context, input, sourceRoot, targetRoot string) (Report, error) {
	manifest, content, err := readExport(input)
	if err != nil {
		return Report{}, err
	}
	sourceRoot, err = validateObjectRoot(sourceRoot, false, "source object root")
	if err != nil {
		return Report{}, err
	}
	targetRoot, err = validateObjectRoot(targetRoot, true, "target object root")
	if err != nil {
		return Report{}, err
	}
	if sameFilesystemPath(sourceRoot, targetRoot) {
		return Report{}, errors.New("source and target object roots must be different")
	}
	for _, object := range content.Objects {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := copyOneObject(sourceRoot, targetRoot, object); err != nil {
			return Report{}, fmt.Errorf("copy object %s: %w", object.TargetKey, err)
		}
	}
	if err := verifyObjects(ctx, targetRoot, content.Objects); err != nil {
		return Report{}, err
	}
	return newReport("copy-objects", manifest, content, true), nil
}

func verifyObjects(ctx context.Context, targetRoot string, objects []Object) error {
	var err error
	targetRoot, err = validateObjectRoot(targetRoot, false, "target object root")
	if err != nil {
		return err
	}
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		metadata, err := inspectObject(targetRoot, object.TargetKey)
		if err != nil {
			return fmt.Errorf("verify target object %s: %w", object.TargetKey, err)
		}
		if metadata.Size != object.Size || metadata.SHA256 != object.SHA256 {
			return fmt.Errorf("target object size or SHA-256 mismatch: %s", object.TargetKey)
		}
	}
	return nil
}

func copyOneObject(sourceRoot, targetRoot string, object Object) error {
	sourcePath, err := resolveObjectPath(sourceRoot, object.SourceKey, false)
	if err != nil {
		return err
	}
	targetPath, err := resolveObjectPath(targetRoot, object.TargetKey, true)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(targetPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing target is not a regular file")
		}
		metadata, hashErr := inspectObject(targetRoot, object.TargetKey)
		if hashErr != nil {
			return hashErr
		}
		if metadata.Size != object.Size || metadata.SHA256 != object.SHA256 {
			return errors.New("existing target has different content")
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".canvas-object-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(source, object.Size+1))
	if err != nil {
		return err
	}
	if written != object.Size || hex.EncodeToString(hasher.Sum(nil)) != object.SHA256 {
		return errors.New("source object size or SHA-256 mismatch")
	}
	var extra [1]byte
	if count, readErr := source.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return errors.New("source object grew while being copied")
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return err
	}
	committed = true
	return syncDirectory(filepath.Dir(targetPath))
}

type objectMetadata struct {
	Size        int64
	SHA256      string
	ContentType string
}

func inspectObject(root, key string) (objectMetadata, error) {
	resolved, err := resolveObjectPath(root, key, false)
	if err != nil {
		return objectMetadata{}, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return objectMetadata{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return objectMetadata{}, err
	}
	if !info.Mode().IsRegular() {
		return objectMetadata{}, errors.New("object is not a regular file")
	}
	hasher := sha256.New()
	prefix := make([]byte, 512)
	read, err := io.ReadFull(file, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return objectMetadata{}, err
	}
	prefix = prefix[:read]
	if _, err := hasher.Write(prefix); err != nil {
		return objectMetadata{}, err
	}
	if _, err := io.Copy(hasher, file); err != nil {
		return objectMetadata{}, err
	}
	contentType := http.DetectContentType(prefix)
	if mediaType := strings.SplitN(contentType, ";", 2)[0]; mediaType != "" {
		contentType = mediaType
	}
	return objectMetadata{Size: info.Size(), SHA256: hex.EncodeToString(hasher.Sum(nil)), ContentType: contentType}, nil
}

func validateObjectRoot(value string, create bool, label string) (string, error) {
	cleaned, err := cleanAbsolutePath(value, label)
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(cleaned, 0700); err != nil {
			return "", fmt.Errorf("create %s: %w", label, err)
		}
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s must be a real directory", label)
	}
	return cleaned, nil
}

func resolveObjectPath(root, key string, createParents bool) (string, error) {
	if !validRelativeKey(key) {
		return "", errors.New("invalid relative object key")
	}
	parts := strings.Split(key, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && createParents {
			if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("object path contains a symlink or non-directory component")
		}
	}
	result := filepath.Join(current, parts[len(parts)-1])
	relative, err := filepath.Rel(root, result)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("object path escapes its root")
	}
	return result, nil
}

func sameFilesystemPath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil {
		left = leftResolved
	}
	if rightErr == nil {
		right = rightResolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func newReport(operation string, manifest Manifest, content dataset, verified bool) Report {
	return Report{
		Format: ReportFormat, Operation: operation, Verified: verified,
		ContentSHA256: manifest.ContentSHA256, Counts: datasetCounts(content),
		ObjectCount: len(content.Objects), ObjectBytes: manifest.Objects.Bytes,
		CompletedAt: time.Now().UTC(),
	}
}
