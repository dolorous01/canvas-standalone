package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

type Role string

const (
	RoleAPI     Role = "api"
	RoleWorker  Role = "worker"
	RoleMigrate Role = "migrate"
)

type Config struct {
	Role                       Role
	ListenAddress              string
	DatabaseURL                string
	OfficialBaseURL            string
	ObjectRoot                 string
	Release                    string
	WritesEnabled              bool
	AuthCacheTTL               time.Duration
	CredentialActiveKeyVersion int
	CredentialKeyFiles         map[int]string
	ContainerNetworkListen     bool
	AllowedExternalUserIDs     []int64
}

type lookupEnv func(string) (string, bool)

func Load(role Role) (Config, error) {
	return load(role, os.LookupEnv)
}

func load(role Role, lookup lookupEnv) (Config, error) {
	if role != RoleAPI && role != RoleWorker && role != RoleMigrate {
		return Config{}, errors.New("invalid process role")
	}
	result := Config{
		Role:                   role,
		Release:                envOr(lookup, "CANVAS_RELEASE", "dev"),
		WritesEnabled:          envOr(lookup, "CANVAS_WRITES_ENABLED", "false") == "true",
		AuthCacheTTL:           20 * time.Second,
		ContainerNetworkListen: envOr(lookup, "CANVAS_CONTAINER_NETWORK_LISTEN", "false") == "true",
	}

	databaseURL, err := readSecretSetting(lookup, "CANVAS_DATABASE_URL_FILE", 16<<10)
	if err != nil {
		return Config{}, err
	}
	if err := validateDatabaseURL(databaseURL); err != nil {
		return Config{}, err
	}
	result.DatabaseURL = databaseURL

	if role == RoleMigrate {
		return result, nil
	}

	result.OfficialBaseURL = strings.TrimSpace(envOr(lookup, "CANVAS_OFFICIAL_BASE_URL", "http://127.0.0.1:18080"))
	if err := validateHTTPBaseURL(result.OfficialBaseURL); err != nil {
		return Config{}, err
	}
	result.ObjectRoot = strings.TrimSpace(envOr(lookup, "CANVAS_OBJECT_ROOT", ""))
	if !filepath.IsAbs(result.ObjectRoot) {
		return Config{}, errors.New("CANVAS_OBJECT_ROOT must be an absolute path")
	}

	if role == RoleAPI || role == RoleWorker {
		fallback := "127.0.0.1:18101"
		if role == RoleWorker {
			fallback = "127.0.0.1:18102"
		}
		result.ListenAddress = envOr(lookup, "CANVAS_LISTEN_ADDRESS", fallback)
		if err := validateListenAddress(result.ListenAddress, result.ContainerNetworkListen); err != nil {
			return Config{}, err
		}
	}

	if value := strings.TrimSpace(envOr(lookup, "CANVAS_AUTH_CACHE_TTL", "20s")); value != "" {
		duration, parseErr := time.ParseDuration(value)
		if parseErr != nil || duration < 0 || duration > 30*time.Second {
			return Config{}, errors.New("CANVAS_AUTH_CACHE_TTL must be between 0s and 30s")
		}
		result.AuthCacheTTL = duration
	}

	activeVersion, err := strconv.Atoi(envOr(lookup, "CANVAS_CREDENTIAL_ACTIVE_KEY_VERSION", "0"))
	if err != nil || activeVersion < 0 {
		return Config{}, errors.New("CANVAS_CREDENTIAL_ACTIVE_KEY_VERSION must be a non-negative integer")
	}
	result.CredentialActiveKeyVersion = activeVersion
	result.CredentialKeyFiles = make(map[int]string)
	for version := 1; version <= 32; version++ {
		name := fmt.Sprintf("CANVAS_CREDENTIAL_KEY_V%d_FILE", version)
		value := strings.TrimSpace(envOr(lookup, name, ""))
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			return Config{}, fmt.Errorf("%s must be an absolute path", name)
		}
		result.CredentialKeyFiles[version] = value
	}
	if activeVersion > 0 {
		if _, ok := result.CredentialKeyFiles[activeVersion]; !ok {
			return Config{}, errors.New("active credential key file is not configured")
		}
	}
	if result.WritesEnabled && activeVersion == 0 {
		return Config{}, errors.New("writes require an active credential encryption key")
	}
	result.AllowedExternalUserIDs, err = parsePositiveIDs(envOr(lookup, "CANVAS_ALLOWED_EXTERNAL_USER_IDS", ""))
	if err != nil {
		return Config{}, err
	}
	return result, nil
}

func parsePositiveIDs(value string) ([]int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	seen := make(map[int64]struct{})
	result := make([]int64, 0)
	for _, item := range strings.Split(value, ",") {
		identifier, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		if err != nil || identifier <= 0 {
			return nil, errors.New("CANVAS_ALLOWED_EXTERNAL_USER_IDS must be a comma-separated list of positive integers")
		}
		if _, exists := seen[identifier]; exists {
			return nil, errors.New("CANVAS_ALLOWED_EXTERNAL_USER_IDS must not contain duplicates")
		}
		seen[identifier] = struct{}{}
		result = append(result, identifier)
	}
	return result, nil
}

func readSecretSetting(lookup lookupEnv, name string, maxSize int64) (string, error) {
	path := strings.TrimSpace(envOr(lookup, name, ""))
	if path == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	payload, err := secretfile.Read(path, maxSize)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	defer secretfile.Zero(payload)
	return string(payload), nil
}

func envOr(lookup lookupEnv, name, fallback string) string {
	if value, ok := lookup(name); ok {
		return value
	}
	return fallback
}

func validateDatabaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return errors.New("database secret must contain a PostgreSQL URL")
	}
	return nil
}

func validateHTTPBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("CANVAS_OFFICIAL_BASE_URL must be an absolute HTTP URL without credentials, query, or fragment")
	}
	return nil
}

func validateListenAddress(value string, allowContainerWildcard bool) error {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("CANVAS_LISTEN_ADDRESS must include a valid host and port")
	}
	ip := net.ParseIP(host)
	if allowContainerWildcard && ip != nil && ip.IsUnspecified() {
		return nil
	}
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("CANVAS_LISTEN_ADDRESS must bind to loopback unless CANVAS_CONTAINER_NETWORK_LISTEN=true")
	}
	return nil
}
