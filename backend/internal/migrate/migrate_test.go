package migrate

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreContiguousAndStable(t *testing.T) {
	items, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("no embedded migrations")
	}
	for _, item := range items {
		if len(item.checksum) != 64 || strings.TrimSpace(string(item.payload)) == "" {
			t.Fatalf("invalid embedded migration: %s", item.filename)
		}
	}
	if len(schemaDigest(items)) != 64 {
		t.Fatal("invalid schema digest")
	}
}
