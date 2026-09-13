package library

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeLibraryItem(t *testing.T) {
	item, err := normalize(Write{
		ClientID: " local-1 ", Kind: "TEXT", Title: " Prompt ", Content: "Hello",
		Tags: []string{"portrait", " Portrait ", "lighting"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if item.ClientID != "local-1" || item.Kind != "text" || len(item.Tags) != 2 || string(item.Metadata) != "{}" {
		t.Fatalf("normalized item = %+v", item)
	}
}

func TestNormalizeLibraryItemRejectsUnsafeShapes(t *testing.T) {
	tests := []Write{
		{Kind: "text", Title: "No client", Content: "x"},
		{ClientID: "x", Kind: "text", Title: "No content"},
		{ClientID: "x", Kind: "image", Title: "No asset"},
		{ClientID: "x", Kind: "text", Title: "Prompt", Content: "x", Metadata: []byte("[]")},
		{ClientID: "x", Kind: "text", Title: "Prompt", Content: strings.Repeat("x", (1<<20)+1)},
	}
	for _, input := range tests {
		if _, err := normalize(input, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid item accepted: %+v; err=%v", input, err)
		}
	}
}
