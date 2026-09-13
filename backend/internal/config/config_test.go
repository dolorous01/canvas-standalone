package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRequiresSecretFileAndLoopback(t *testing.T) {
	directory := t.TempDir()
	dsnPath := filepath.Join(directory, "database-url")
	if err := os.WriteFile(dsnPath, []byte("postgres://canvas:secret@database/canvas\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"CANVAS_DATABASE_URL_FILE": dsnPath,
		"CANVAS_OBJECT_ROOT":       filepath.Join(directory, "objects"),
		"CANVAS_LISTEN_ADDRESS":    "127.0.0.1:18101",
	}
	configuration, err := load(RoleAPI, mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DatabaseURL == "" || configuration.ListenAddress != "127.0.0.1:18101" {
		t.Fatalf("unexpected configuration: %+v", configuration)
	}

	values["CANVAS_LISTEN_ADDRESS"] = "0.0.0.0:18101"
	if _, err := load(RoleAPI, mapLookup(values)); err == nil {
		t.Fatal("public bind address was accepted")
	}
	values["CANVAS_CONTAINER_NETWORK_LISTEN"] = "true"
	if _, err := load(RoleAPI, mapLookup(values)); err != nil {
		t.Fatalf("container wildcard bind was rejected: %v", err)
	}
}

func TestWorkerHasIndependentHealthListenAddress(t *testing.T) {
	directory := t.TempDir()
	dsnPath := filepath.Join(directory, "database-url")
	if err := os.WriteFile(dsnPath, []byte("postgres://canvas:secret@database/canvas"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := load(RoleWorker, mapLookup(map[string]string{
		"CANVAS_DATABASE_URL_FILE": dsnPath,
		"CANVAS_OBJECT_ROOT":       filepath.Join(directory, "objects"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ListenAddress != "127.0.0.1:18102" {
		t.Fatalf("unexpected worker listen address: %s", configuration.ListenAddress)
	}
}

func TestLoadParsesCandidateUserAllowlist(t *testing.T) {
	directory := t.TempDir()
	dsnPath := filepath.Join(directory, "database-url")
	if err := os.WriteFile(dsnPath, []byte("postgres://canvas:secret@database/canvas"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"CANVAS_DATABASE_URL_FILE":         dsnPath,
		"CANVAS_OBJECT_ROOT":               filepath.Join(directory, "objects"),
		"CANVAS_ALLOWED_EXTERNAL_USER_IDS": "42, 77",
	}
	configuration, err := load(RoleAPI, mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.AllowedExternalUserIDs) != 2 || configuration.AllowedExternalUserIDs[0] != 42 || configuration.AllowedExternalUserIDs[1] != 77 {
		t.Fatalf("unexpected allowlist: %#v", configuration.AllowedExternalUserIDs)
	}
	values["CANVAS_ALLOWED_EXTERNAL_USER_IDS"] = "42,42"
	if _, err := load(RoleAPI, mapLookup(values)); err == nil {
		t.Fatal("duplicate allowlist entry was accepted")
	}
}

func TestLoadRejectsDirectDatabaseValueAndMissingActiveKey(t *testing.T) {
	values := map[string]string{
		"CANVAS_DATABASE_URL":                  "postgres://leaked:secret@database/canvas",
		"CANVAS_OBJECT_ROOT":                   "/tmp/canvas-objects",
		"CANVAS_CREDENTIAL_ACTIVE_KEY_VERSION": "2",
	}
	if _, err := load(RoleWorker, mapLookup(values)); err == nil {
		t.Fatal("configuration without CANVAS_DATABASE_URL_FILE was accepted")
	}

	directory := t.TempDir()
	dsnPath := filepath.Join(directory, "database-url")
	if err := os.WriteFile(dsnPath, []byte("postgres://canvas:secret@database/canvas"), 0o600); err != nil {
		t.Fatal(err)
	}
	values["CANVAS_DATABASE_URL_FILE"] = dsnPath
	if _, err := load(RoleWorker, mapLookup(values)); err == nil {
		t.Fatal("configuration without the active key file was accepted")
	}
}

func mapLookup(values map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
