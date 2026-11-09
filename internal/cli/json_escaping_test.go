package cli

import (
	"bytes"
	"encoding/json"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"testing"
)

func TestSharedJSONPreservesEndpointEscaping(t *testing.T) {
	var native, controller bytes.Buffer
	if err := writeJSON(&native, map[string]any{"value": "<&>"}); err != nil {
		t.Fatal(err)
	}
	if got, want := native.String(), "{\n  \"value\": \"<&>\"\n}\n"; got != want {
		t.Fatalf("native JSON bytes changed: %q", got)
	}
	if err := writeFleetResults(&controller, []fleet.Result{{Host: "<&>", State: "ok"}}, "json"); err != nil {
		t.Fatal(err)
	}
	// Compare the existing default-escaping encoder, preserving field order and newline.
	var legacy bytes.Buffer
	enc := json.NewEncoder(&legacy)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"hosts": []fleet.Result{{Host: "<&>", State: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(controller.Bytes(), legacy.Bytes()) {
		t.Fatalf("fleet JSON bytes changed:\n%s\n%s", legacy.String(), controller.String())
	}
}
