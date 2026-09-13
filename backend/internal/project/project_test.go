package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateDocumentExtractsOwnedReferences(t *testing.T) {
	payload := json.RawMessage(`{
		"schema_version": 2,
		"nodes": [
			{"id":"node-1","type":"image","position":{"x":0,"y":0},"asset_id":"asset_abc"},
			{"id":"node-2","type":"group","position":{"x":1,"y":1},"metadata":{"result":{"asset_id":"asset_def"}}}
		],
		"edges": []
	}`)
	references, err := ValidateDocument(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 2 || references[0].NodeID != "node-1" || references[1].PublicAssetID != "asset_def" {
		t.Fatalf("references = %+v", references)
	}
}

func TestValidateDocumentRejectsSecretsExecutableDataAndLimits(t *testing.T) {
	tests := []string{
		`{"schema_version":1,"nodes":[],"api_key":"secret"}`,
		`{"schema_version":1,"nodes":[{"id":"n","text":"Bearer token"}]}`,
		`{"schema_version":1,"nodes":[{"id":"n","url":"javascript:alert(1)"}]}`,
		`{"schema_version":3,"nodes":[]}`,
		`{"schema_version":1,"nodes":"invalid"}`,
		`{"schema_version":1,"nodes":[]} trailing`,
	}
	for _, payload := range tests {
		if _, err := ValidateDocument(json.RawMessage(payload)); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("invalid document accepted: %s; err=%v", payload, err)
		}
	}
	large := fmt.Sprintf(`{"schema_version":1,"nodes":[{"id":"n","text":%q}]}`, strings.Repeat("x", 257<<10))
	if _, err := ValidateDocument(json.RawMessage(large)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatal("oversized string was accepted")
	}
}
