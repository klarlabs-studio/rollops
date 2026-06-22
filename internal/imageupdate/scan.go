package imageupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Scanner lists the tags a container repository publishes, via the Docker
// Registry v2 API and its bearer-token challenge flow (works for ghcr.io,
// docker.io, and compatible registries). Credentials are optional — set them for
// private repositories. This is what lets Rollops poll for new image versions
// the way keel does, instead of waiting for an external trigger.
type Scanner struct {
	HTTP     *http.Client
	Username string // optional, for private registries
	Password string // optional token/PAT
}

func (s Scanner) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// ParseRef splits an image reference into registry host and repository path,
// dropping any tag. ghcr.io/acme/app:v1 → ("ghcr.io", "acme/app").
func ParseRef(image string) (host, repo string) {
	ref := image
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i] // strip :tag
	}
	first, rest, ok := strings.Cut(ref, "/")
	if ok && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		return first, rest
	}
	// No registry host in the ref → Docker Hub.
	if !strings.Contains(ref, "/") {
		return "docker.io", "library/" + ref
	}
	return "docker.io", ref
}

type tagsResponse struct {
	Tags []string `json:"tags"`
}

// Tags returns the published tags of the image's repository.
func (s Scanner) Tags(ctx context.Context, image string) ([]string, error) {
	host, repo := ParseRef(image)
	base := "https://" + host
	if host == "docker.io" {
		base = "https://registry-1.docker.io"
	}
	url := fmt.Sprintf("%s/v2/%s/tags/list", base, repo)
	body, err := s.get(ctx, url, repo)
	if err != nil {
		return nil, err
	}
	var tr tagsResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("imageupdate: parse tags: %w", err)
	}
	return tr.Tags, nil
}

var bearerRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// get performs an authenticated registry GET, handling the v2 bearer challenge:
// an unauthenticated request returns 401 with a WWW-Authenticate header naming a
// token endpoint; we fetch a token (with optional basic creds) and retry.
func (s Scanner) get(ctx context.Context, url, repo string) ([]byte, error) {
	resp, err := s.do(ctx, url, "")
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusUnauthorized {
		token, terr := s.token(ctx, resp.authenticate, repo)
		if terr != nil {
			return nil, terr
		}
		resp, err = s.do(ctx, url, token)
		if err != nil {
			return nil, err
		}
	}
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("imageupdate: registry %s: status %d", url, resp.status)
	}
	return resp.body, nil
}

type regResp struct {
	status       int
	authenticate string
	digest       string
	body         []byte
}

func (s Scanner) do(ctx context.Context, url, bearer string) (regResp, error) {
	return s.doMethod(ctx, http.MethodGet, url, bearer)
}

func (s Scanner) doMethod(ctx context.Context, method, url, bearer string) (regResp, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return regResp{}, err
	}
	// Accept both OCI and Docker manifest media types so a digest resolves for
	// single-arch and multi-arch (index) images alike.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return regResp{}, fmt.Errorf("imageupdate: registry request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return regResp{
		status:       resp.StatusCode,
		authenticate: resp.Header.Get("WWW-Authenticate"),
		digest:       resp.Header.Get("Docker-Content-Digest"),
		body:         body,
	}, nil
}

// Digest resolves the manifest digest (sha256:…) of an image reference's tag,
// the immutable identity a mutable tag (latest) currently points at. It lets
// image automation pin a moving tag and redeploy when its digest changes — the
// keel "force" model, GitOps-native.
func (s Scanner) Digest(ctx context.Context, image string) (string, error) {
	host, repo := ParseRef(image)
	tag := "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		tag = image[i+1:]
	}
	base := "https://" + host
	if host == "docker.io" {
		base = "https://registry-1.docker.io"
	}
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, tag)
	resp, err := s.doMethod(ctx, http.MethodHead, url, "")
	if err != nil {
		return "", err
	}
	if resp.status == http.StatusUnauthorized {
		token, terr := s.token(ctx, resp.authenticate, repo)
		if terr != nil {
			return "", terr
		}
		if resp, err = s.doMethod(ctx, http.MethodHead, url, token); err != nil {
			return "", err
		}
	}
	if resp.status != http.StatusOK {
		return "", fmt.Errorf("imageupdate: manifest %s: status %d", url, resp.status)
	}
	if resp.digest == "" {
		return "", fmt.Errorf("imageupdate: no Docker-Content-Digest for %s", image)
	}
	return resp.digest, nil
}

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

// token fulfils a Bearer challenge: parse realm/service/scope, GET the realm
// with the scope (and basic creds when configured), and return the token.
func (s Scanner) token(ctx context.Context, challenge, repo string) (string, error) {
	params := map[string]string{}
	for _, m := range bearerRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("imageupdate: no token realm in challenge %q", challenge)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	url := fmt.Sprintf("%s?service=%s&scope=%s", realm, params["service"], scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if s.Username != "" || s.Password != "" {
		req.SetBasicAuth(s.Username, s.Password)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("imageupdate: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imageupdate: token endpoint status %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("imageupdate: parse token: %w", err)
	}
	if tr.Token != "" {
		return tr.Token, nil
	}
	return tr.AccessToken, nil
}
