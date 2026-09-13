package editor

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateEditorDocumentAndReferences(t *testing.T) {
	document := json.RawMessage(`{
		"schema_version":1,
		"viewport":{"zoom":1,"x":0,"y":0},
		"canvas":{"width":1024,"height":768,"background":"transparent"}
	}`)
	if err := validateDocument(document); err != nil {
		t.Fatal(err)
	}
	references, err := normalizeReferences([]AssetReference{
		{AssetPublicID: "asset_one", Role: "SOURCE", ElementID: "layer-1"},
		{AssetPublicID: "asset_one", Role: "source", ElementID: "layer-1"},
	})
	if err != nil || len(references) != 1 {
		t.Fatalf("references = %+v, %v", references, err)
	}
}

func TestValidateEditorDocumentRejectsUnsafeOrInvalidData(t *testing.T) {
	tests := []json.RawMessage{
		json.RawMessage(`{"schema_version":2,"viewport":{"zoom":1,"x":0,"y":0},"canvas":{"width":1,"height":1,"background":"white"}}`),
		json.RawMessage(`{"schema_version":1,"viewport":{"zoom":0,"x":0,"y":0},"canvas":{"width":1,"height":1,"background":"white"}}`),
		json.RawMessage(`{"schema_version":1,"viewport":{"zoom":1,"x":0,"y":0},"canvas":{"width":1,"height":1,"background":"white"},"secret":"value"}`),
	}
	for _, document := range tests {
		if err := validateDocument(document); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid editor document accepted: %s; err=%v", document, err)
		}
	}
	if _, err := validateParameters(json.RawMessage(`{"api_key":"value"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe parameters accepted")
	}
}
