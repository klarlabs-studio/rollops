package imageupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	endpoint := fmt.Sprintf("%s/v2/%s/tags/list", base, repo)

	// The Docker Registry v2 tags/list endpoint is PAGINATED (ghcr.io caps a
	// page at 100 tags) and advertises the next page via an RFC 5988 Link header
	// (`<…?last=…>; rel="next"`). A repository with more than one page of tags —
	// which any long-lived service accrues — would otherwise expose only its
	// FIRST page, and since that page is not version-sorted the newest tags fall
	// off it entirely. Image automation then never sees a newer release and
	// silently reports "current" forever. Follow every `rel="next"` link and
	// accumulate all pages so SelectTag compares against the complete tag set.
	var all []string
	// pageCap bounds a pathological/looping registry (defensive; real repos need
	// far fewer). 1000 pages × 100 tags = 100k tags — orders beyond any real repo.
	const pageCap = 1000
	for i := 0; endpoint != "" && i < pageCap; i++ {
		body, link, err := s.get(ctx, endpoint, repo, host)
		if err != nil {
			return nil, err
		}
		var tr tagsResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			return nil, fmt.Errorf("imageupdate: parse tags: %w", err)
		}
		all = append(all, tr.Tags...)
		endpoint = nextPageURL(link, base)
	}
	return all, nil
}

// nextPageURL extracts the `rel="next"` target from a Docker Registry v2 Link
// header and resolves it against base, or returns "" when there is no next page.
// The registry emits the next link as an absolute path (`</v2/…/tags/list?last=…>`)
// which must be joined to the registry host; an absolute URL is used verbatim.
func nextPageURL(link, base string) string {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		openIdx := strings.IndexByte(part, '<')
		closeIdx := strings.IndexByte(part, '>')
		if openIdx < 0 || closeIdx <= openIdx {
			continue
		}
		target := part[openIdx+1 : closeIdx]
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			return target
		}
		return base + target
	}
	return ""
}

var bearerRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// get performs an authenticated registry GET, handling the v2 bearer challenge:
// an unauthenticated request returns 401 with a WWW-Authenticate header naming a
// token endpoint; we fetch a token (with optional basic creds) and retry.
// registryHost is the host of the image being scanned; creds are bound to it so
// they are never sent to an attacker-named token realm on a different host.
func (s Scanner) get(ctx context.Context, endpoint, repo, registryHost string) (body []byte, link string, err error) {
	resp, err := s.do(ctx, endpoint, "")
	if err != nil {
		return nil, "", err
	}
	if resp.status == http.StatusUnauthorized {
		token, terr := s.token(ctx, resp.authenticate, repo, registryHost)
		if terr != nil {
			return nil, "", terr
		}
		resp, err = s.do(ctx, endpoint, token)
		if err != nil {
			return nil, "", err
		}
	}
	if resp.status != http.StatusOK {
		return nil, "", fmt.Errorf("imageupdate: registry %s: status %d", endpoint, resp.status)
	}
	return resp.body, resp.link, nil
}

type regResp struct {
	status       int
	authenticate string
	digest       string
	link         string
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
		link:         resp.Header.Get("Link"),
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
	endpoint := fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, tag)
	resp, err := s.doMethod(ctx, http.MethodHead, endpoint, "")
	if err != nil {
		return "", err
	}
	if resp.status == http.StatusUnauthorized {
		token, terr := s.token(ctx, resp.authenticate, repo, host)
		if terr != nil {
			return "", terr
		}
		if resp, err = s.doMethod(ctx, http.MethodHead, endpoint, token); err != nil {
			return "", err
		}
	}
	if resp.status != http.StatusOK {
		return "", fmt.Errorf("imageupdate: manifest %s: status %d", endpoint, resp.status)
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
//
// The realm comes from the registry's WWW-Authenticate header and is therefore
// attacker-influenced: a repo naming `image: attacker.example/foo` can point the
// challenge at any URL. Basic creds (a registry PAT) are attached ONLY when the
// realm is on the same host as the image's registry AND served over https, so a
// hostile realm on evil.com — or the same host over cleartext http — never
// receives the credential. A cross-host realm is still followed, but
// unauthenticated (public pulls keep working; a leak is what we prevent).
func (s Scanner) token(ctx context.Context, challenge, repo, registryHost string) (string, error) {
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
	endpoint := fmt.Sprintf("%s?service=%s&scope=%s", realm, params["service"], scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	if (s.Username != "" || s.Password != "") && realmTrusted(realm, registryHost) {
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

// realmTrusted reports whether it is safe to attach the registry credential to a
// bearer-token realm: the realm must be served over https and be on the same
// host as the image's registry. Docker Hub is the one exception — its registry
// (registry-1.docker.io / docker.io) delegates tokens to a dedicated first-party
// auth host (auth.docker.io) — so its own hosts are treated as one trust domain.
func realmTrusted(realm, registryHost string) bool {
	u, err := url.Parse(realm)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	if u.Host == registryHost {
		return true
	}
	if isDockerHub(u.Hostname()) && isDockerHub(registryHost) {
		return true
	}
	return false
}

// isDockerHub reports whether host is one of Docker's own first-party hosts
// (registry endpoints and the token auth host), which form a single trust domain.
func isDockerHub(host string) bool {
	switch host {
	case "docker.io", "registry-1.docker.io", "index.docker.io", "auth.docker.io":
		return true
	}
	return false
}
