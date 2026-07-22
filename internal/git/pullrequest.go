package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// pull-request writeback.
//
// When image automation cannot push to the tracked branch — a protected branch
// requiring reviews or status checks — it proposes the bump as a pull request
// instead. rollops opens (or refreshes) the PR and, when the repository permits
// it, enables GitHub's native auto-merge so the change lands the moment its
// required checks pass. The deploy still happens through the ordinary reconcile
// once the PR merges and the tracked branch advances; nothing here deploys.

// OpenPullRequest ensures a PR proposes headBranch be merged into base, with the
// given title and body. It is idempotent: an existing open PR for headBranch is
// reused rather than duplicated. When the repository allows auto-merge, it is
// enabled so the PR lands automatically once required checks pass.
//
// Returns the PR's URL and whether auto-merge was enabled. Auto-merge failing to
// enable is not an error — the PR is open and can be merged by a human — so a
// repo without auto-merge still gets unblocked, just not hands-free.
func (s *Source) OpenPullRequest(ctx context.Context, headBranch, base, title, body string) (prURL string, autoMerge bool, err error) {
	owner, repo, err := ownerRepoFromURL(s.url)
	if err != nil {
		return "", false, err
	}
	nodeID, url, err := s.createOrFindPR(ctx, owner, repo, headBranch, base, title, body)
	if err != nil {
		return "", false, err
	}
	if merr := s.enableAutoMerge(ctx, nodeID); merr == nil {
		autoMerge = true
	}
	return url, autoMerge, nil
}

// createOrFindPR opens a PR and returns its node id and URL. A 422 whose body
// names an existing PR is treated as success — GitHub returns it when a PR for
// the same head already exists — and the open PR is looked up and reused.
func (s *Source) createOrFindPR(ctx context.Context, owner, repo, head, base, title, body string) (nodeID, url string, err error) {
	payload, _ := json.Marshal(map[string]string{"title": title, "head": head, "base": base, "body": body})
	status, respBody, err := s.githubAPI(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), payload)
	if err != nil {
		return "", "", err
	}
	if status == http.StatusCreated {
		var pr struct {
			NodeID  string `json:"node_id"`
			HTMLURL string `json:"html_url"`
		}
		if uerr := json.Unmarshal(respBody, &pr); uerr != nil {
			return "", "", fmt.Errorf("git: parse created PR: %w", uerr)
		}
		return pr.NodeID, pr.HTMLURL, nil
	}
	// Already exists → find and reuse it. GitHub returns 422 for a duplicate head.
	if status == http.StatusUnprocessableEntity && bytes.Contains(bytes.ToLower(respBody), []byte("already exist")) {
		return s.findOpenPR(ctx, owner, repo, head)
	}
	return "", "", fmt.Errorf("git: open PR %s/%s (%s→%s): status %d: %s",
		owner, repo, head, base, status, bytes.TrimSpace(respBody))
}

// findOpenPR resolves the open PR whose head is headBranch.
func (s *Source) findOpenPR(ctx context.Context, owner, repo, head string) (nodeID, url string, err error) {
	status, respBody, err := s.githubAPI(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s", owner, repo, owner, head), nil)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusOK {
		return "", "", fmt.Errorf("git: list PRs for %s: status %d: %s", head, status, bytes.TrimSpace(respBody))
	}
	var prs []struct {
		NodeID  string `json:"node_id"`
		HTMLURL string `json:"html_url"`
	}
	if uerr := json.Unmarshal(respBody, &prs); uerr != nil {
		return "", "", fmt.Errorf("git: parse PR list: %w", uerr)
	}
	if len(prs) == 0 {
		return "", "", fmt.Errorf("git: PR for head %s reported existing but none found open", head)
	}
	return prs[0].NodeID, prs[0].HTMLURL, nil
}

// enableAutoMerge turns on GitHub's native auto-merge for the PR via GraphQL, so
// it merges itself once required checks pass. It fails (returns an error the
// caller tolerates) when the repository has auto-merge disabled or the token
// lacks permission — in which case the PR simply waits for a manual merge.
func (s *Source) enableAutoMerge(ctx context.Context, prNodeID string) error {
	if prNodeID == "" {
		return fmt.Errorf("git: auto-merge: empty PR node id")
	}
	query := map[string]any{
		"query":     `mutation($id:ID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:SQUASH}){clientMutationId}}`,
		"variables": map[string]string{"id": prNodeID},
	}
	payload, _ := json.Marshal(query)
	status, respBody, err := s.githubAPI(ctx, http.MethodPost, "/graphql", payload)
	if err != nil {
		return err
	}
	// GraphQL returns 200 even for logical errors, reported in an "errors" array.
	if status != http.StatusOK || bytes.Contains(respBody, []byte(`"errors"`)) {
		return fmt.Errorf("git: enable auto-merge: status %d: %s", status, bytes.TrimSpace(respBody))
	}
	return nil
}

// githubAPI performs one authenticated GitHub API call and returns the status
// and body. The path is either REST (/repos/...) or "/graphql"; both hang off
// the same host. The installation/PAT token is resolved per call so a rotating
// credential is honoured, and is never written to disk or logged.
func (s *Source) githubAPI(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	token, err := s.auth.token(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("git: resolve token: %w", err)
	}
	base := s.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("git: github api %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, nil
}

// ownerRepoFromURL parses "owner" and "repo" out of a GitHub remote URL in
// https, ssh, or git form. It is deliberately strict: pull-request writeback
// only targets GitHub, and anything else should fail loudly rather than post to
// the wrong place.
func ownerRepoFromURL(url string) (owner, repo string, err error) {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, ".git")
	switch {
	case strings.HasPrefix(u, "git@"): // git@github.com:owner/repo
		if i := strings.Index(u, ":"); i >= 0 {
			u = u[i+1:]
		}
	case strings.Contains(u, "://"): // https://github.com/owner/repo, ssh://git@github.com/owner/repo
		if i := strings.Index(u, "://"); i >= 0 {
			u = u[i+3:]
		}
		if i := strings.Index(u, "/"); i >= 0 {
			u = u[i+1:] // drop host
		}
		u = strings.TrimPrefix(u, "git@")
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", "", fmt.Errorf("git: cannot parse owner/repo from URL %q", url)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}
