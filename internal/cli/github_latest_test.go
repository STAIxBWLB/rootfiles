package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchLatestVersionPreservesDefaultClientAndLegacyErrors(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	for _, tc := range []struct {
		body, want string
		status     int
	}{
		{`{}`, "GitHub API returned 503", 503},
		{`{}`, "no tag_name in release response", 200},
		{`{"tag_name":"v1.2.3 preview "}`, "", 200},
	} {
		http.DefaultClient = &http.Client{Timeout: 23 * time.Second, Transport: updateCheckTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://api.github.com/repos/STAIxBWLB/rootfiles/releases/latest" {
				t.Errorf("rootfiles endpoint changed: %s", request.URL)
			}
			if deadline, ok := request.Context().Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 23*time.Second {
				t.Errorf("rootfiles replaced caller client timeout: %v, %t", deadline, ok)
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})}
		tag, err := fetchLatestVersion(context.Background())
		if tc.want != "" {
			if err == nil || err.Error() != tc.want {
				t.Fatalf("rootfiles error=%v, want %q", err, tc.want)
			}
		} else if err != nil || tag != "v1.2.3 preview " {
			t.Fatalf("rootfiles tag=%q error=%v", tag, err)
		}
	}
}
