package publicid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
)

var pattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func New(prefix string) (string, error) {
	if !pattern.MatchString(prefix) || len(prefix) > 16 {
		return "", errors.New("invalid public ID prefix")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate public ID: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(random), nil
}

func Valid(value string) bool {
	return pattern.MatchString(value)
}
