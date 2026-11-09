package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// GitHubStatusError identifies an HTTP response failure independently of each
// command's established user-facing error spelling.
type GitHubStatusError struct{ StatusCode int }

func (e *GitHubStatusError) Error() string {
	return fmt.Sprintf("GitHub API returned %d", e.StatusCode)
}

// ErrGitHubTagMissing distinguishes a successful response without a tag.
var ErrGitHubTagMissing = errors.New("no tag_name in release response")

// FetchLatestGitHubTag fetches only the release tag. Callers retain their client
// and context deadlines; a nil client uses http.DefaultClient.
func FetchLatestGitHubTag(ctx context.Context, client *http.Client, apiBase, owner, repo string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", strings.TrimRight(apiBase, "/"), owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", &GitHubStatusError{StatusCode: response.StatusCode}
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", ErrGitHubTagMissing
	}
	return release.TagName, nil
}
