package module

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type githubTransportFunc func(*http.Request) (*http.Response, error)

func (f githubTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type githubResponseBody struct {
	io.Reader
	closed bool
}

func (b *githubResponseBody) Close() error { b.closed = true; return nil }

func TestFetchLatestGitHubTagUsesCallerTransportAndContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	body := &githubResponseBody{Reader: strings.NewReader(`{"tag_name":"v1.2.3 preview "}`)}
	client := &http.Client{Transport: githubTransportFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://api.example.test/repos/owner/project/releases/latest" || request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("unexpected release request: %s %s headers=%v", request.Method, request.URL, request.Header)
		}
		if got, ok := request.Context().Deadline(); !ok || !got.Equal(deadline) {
			t.Errorf("caller deadline changed: %v, %t", got, ok)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})}
	tag, err := FetchLatestGitHubTag(ctx, client, "https://api.example.test", "owner", "project")
	if err != nil || tag != "v1.2.3 preview " || !body.closed {
		t.Fatalf("tag=%q error=%v body closed=%t", tag, err, body.closed)
	}
}

func TestFetchLatestGitHubTagPreservesResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"HTTP status", `{}`, "GitHub API returned 503", 503},
		{"missing tag", `{}`, "no tag_name in release response", 200},
		{"empty tag", `{"tag_name":""}`, "no tag_name in release response", 200},
		{"invalid JSON", `{`, "unexpected EOF", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &githubResponseBody{Reader: strings.NewReader(tc.body)}
			client := &http.Client{Transport: githubTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body}, nil
			})}
			if _, err := FetchLatestGitHubTag(context.Background(), client, "https://api.example.test", "owner", "project"); err == nil || err.Error() != tc.want || !body.closed {
				t.Fatalf("error=%v, want %q; body closed=%t", err, tc.want, body.closed)
			}
		})
	}
	transportErr := errors.New("fixture transport failure")
	client := &http.Client{Transport: githubTransportFunc(func(*http.Request) (*http.Response, error) { return nil, transportErr })}
	if _, err := FetchLatestGitHubTag(context.Background(), client, "https://api.example.test", "owner", "project"); !errors.Is(err, transportErr) {
		t.Fatalf("transport error was lost: %v", err)
	}
}

func TestFetchLatestCloudflaredVersionPreservesErrorsAndClientTimeout(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, tc := range []struct {
		body, want string
		status     int
	}{
		{`{}`, "github API returned 503", 503},
		{`{}`, "no tag_name in response", 200},
		{`{"tag_name":"v2026.2.0"}`, "", 200},
	} {
		http.DefaultTransport = githubTransportFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://api.github.com/repos/cloudflare/cloudflared/releases/latest" {
				t.Errorf("cloudflared endpoint changed: %s", request.URL)
			}
			if deadline, ok := request.Context().Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 15*time.Second {
				t.Errorf("cloudflared client lost its 15-second timeout: %v, %t", deadline, ok)
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		tag, err := FetchLatestCloudflaredVersion(context.Background())
		if tc.want != "" {
			if err == nil || err.Error() != tc.want {
				t.Fatalf("cloudflared error=%v, want %q", err, tc.want)
			}
		} else if err != nil || tag != "v2026.2.0" {
			t.Fatalf("cloudflared tag=%q error=%v", tag, err)
		}
	}
}
