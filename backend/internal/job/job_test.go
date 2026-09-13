package job

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
)

func TestSelectCapabilityAndValidateRequest(t *testing.T) {
	capabilityJSON := json.RawMessage(`{
		"generation":true,"edit":true,"multi_image":true,"mask":true,
		"max_input_images":2,"max_outputs":4,"sizes":["1024x1024"],
		"qualities":["medium"],"output_formats":["png"],"backgrounds":["auto"],
		"defaults":{"size":"1024x1024","quality":"medium","output_format":"png","background":"auto"}
	}`)
	selected, err := selectCapability(policy.Policy{Enabled: true, Models: []policy.Model{{Model: "image-model", Enabled: true, Capability: capabilityJSON}}}, "image-model", "generation")
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{Operation: "generation", Parameters: applyDefaults(Parameters{N: 1}, selected)}
	if err := validateParameters(input, selected); err != nil {
		t.Fatal(err)
	}
	input.InputAssetIDs = []string{"asset_one"}
	if !errors.Is(validateParameters(input, selected), ErrInvalid) {
		t.Fatal("generation input asset was accepted")
	}
}

func TestParseOutputsRequiresBoundedInlineData(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	outputs, err := parseOutputs([]byte(`{"data":[{"b64_json":"`+payload+`"}]}`), "webp", 1)
	if err != nil || len(outputs) != 1 || outputs[0].mimeType != "image/webp" {
		t.Fatalf("parseOutputs() = %+v, %v", outputs, err)
	}
	for _, body := range []string{
		`{"data":[]}`,
		`{"data":[{"url":"https://example.test/result.png"}]}`,
		`{"data":[{"b64_json":"%%%"}]}`,
	} {
		if _, err := parseOutputs([]byte(body), "png", 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid output accepted: %s; err=%v", body, err)
		}
	}
}
