package policy

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateModelsRequiresUniqueContiguousImageCapabilities(t *testing.T) {
	capability := json.RawMessage(`{"media_kind":"image","generation":true,"edit":false,"max_input_images":0,"max_outputs":1}`)
	models, err := validateModels([]Model{{Model: "image-one", Enabled: true, Position: 0, Capability: capability}})
	if err != nil || len(models) != 1 {
		t.Fatalf("validateModels() = %+v, %v", models, err)
	}
	for _, input := range [][]Model{
		{},
		{{Model: "image-one", Position: 1, Capability: capability}},
		{{Model: "image-one", Position: 0, Capability: capability}, {Model: "IMAGE-ONE", Position: 1, Capability: capability}},
		{{Model: "text-model", Position: 0, Capability: json.RawMessage(`{"media_kind":"text","generation":true,"max_outputs":1}`)}},
	} {
		if _, err := validateModels(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid policy accepted: %+v; err=%v", input, err)
		}
	}
}
