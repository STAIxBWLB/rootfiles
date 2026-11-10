package tenant

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

// This test is intentionally serial: it substitutes the entropy reader for a
// deterministic alphabet-contract fixture and restores it before returning.
func TestGeneratePasswordUsesSharedSaferAlphabet(t *testing.T) {
	const want = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	indices := make([]byte, len(want))
	for i := range indices {
		indices[i] = byte(i)
	}
	old := rand.Reader
	rand.Reader = bytes.NewReader(indices)
	defer func() { rand.Reader = old }()
	got, err := GeneratePassword(len(want))
	if err != nil || got != want {
		t.Fatalf("password alphabet contract changed: got=%q err=%v", got, err)
	}
	if strings.ContainsAny(got, "loOI01") {
		t.Fatal("generated password contains an excluded look-alike character")
	}
}

var errPasswordEntropyFixture = errors.New("fixture entropy unavailable")

type passwordEntropyFailure struct{}

func (passwordEntropyFailure) Read([]byte) (int, error) { return 0, errPasswordEntropyFixture }

func TestGeneratePasswordPropagatesEntropyFailure(t *testing.T) {
	old := rand.Reader
	rand.Reader = passwordEntropyFailure{}
	defer func() { rand.Reader = old }()
	got, err := GeneratePassword(16)
	if got != "" || !errors.Is(err, errPasswordEntropyFixture) {
		t.Fatalf("entropy failure returned password=%q err=%v", got, err)
	}
}

func TestGeneratePasswordKeepsZeroLengthBehavior(t *testing.T) {
	if got, err := GeneratePassword(0); got != "" || err != nil {
		t.Fatalf("zero-length password=%q err=%v", got, err)
	}
}
