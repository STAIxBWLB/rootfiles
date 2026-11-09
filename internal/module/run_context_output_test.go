package module

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestRunContextOutputKeepsNilFallback(t *testing.T) {
	rc := &RunContext{}
	if rc.Output() != os.Stdout {
		t.Fatal("nil output no longer defaults to stdout")
	}
	var out bytes.Buffer
	rc.Out = &out
	if rc.Output() != &out {
		t.Fatal("explicit writer was ignored")
	}
}

func TestRunAllUsesContextWriter(t *testing.T) {
	rc := newDryRunRC(t)
	var out bytes.Buffer
	rc.Out = &out
	result, err := RunAll(context.Background(), []Module{stubModule{name: "pending"}, stubModule{name: "ready", satisfied: true}}, rc)
	if err != nil || len(result) != 2 {
		t.Fatalf("outcomes=%v error=%v", result, err)
	}
	for _, want := range []string{"pending: stub", "ready: already satisfied"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("explicit writer missing %q: %q", want, out.String())
		}
	}
}
