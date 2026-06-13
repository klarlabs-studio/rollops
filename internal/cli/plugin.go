package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"go.klarlabs.de/rollops/internal/registry"
	"go.klarlabs.de/rollops/internal/security"
)

// PluginFetcher retrieves a plugin source (local path or https URL) to a local
// temp file, returning its path. Injectable for tests.
type PluginFetcher func(ctx context.Context, source string) (localPath string, cleanup func(), err error)

// plugin dispatches `rollops plugin <subcommand>`.
func (a *App) plugin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("plugin: subcommand required (search | install)")
	}
	switch args[0] {
	case "search":
		return a.pluginSearch(ctx, args[1:])
	case "install":
		return a.pluginInstall(ctx, args[1:])
	default:
		return fmt.Errorf("plugin: unknown subcommand %q (try: search, install)", args[0])
	}
}

func (a *App) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

// pluginSearch lists plugins in the marketplace registry matching the query.
func (a *App) pluginSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin search", flag.ContinueOnError)
	fs.SetOutput(a.Out)
	regURL := fs.String("registry", registry.URL(), "plugin registry index URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	idx, err := registry.Fetch(ctx, a.httpClient(), *regURL)
	if err != nil {
		return fmt.Errorf("plugin search: %w", err)
	}
	matches := idx.Search(strings.Join(fs.Args(), " "))
	if len(matches) == 0 {
		fmt.Fprintln(a.Out, "no plugins match")
		return nil
	}
	tw := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLATEST\tCAPABILITIES\tDESCRIPTION")
	for _, p := range matches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Latest, strings.Join(p.Capabilities, ","), p.Description)
	}
	return tw.Flush()
}

// pluginInstall fetches a plugin binary, optionally cosign-verifies it, installs
// it to the plugin directory, and prints the sha256 to pin in a rollout spec.
func (a *App) pluginInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	fs.SetOutput(a.Out)
	dir := fs.String("dir", defaultPluginDir(), "directory to install the plugin into")
	name := fs.String("name", "", "installed file name (default: source basename or plugin name)")
	version := fs.String("version", "", "registry plugin version (default: latest)")
	regURL := fs.String("registry", registry.URL(), "plugin registry index URL")
	keyPath := fs.String("cosign-key", "", "cosign public key for key-based signature verification")
	identity := fs.String("cosign-identity", "", "expected signer identity (keyless)")
	issuer := fs.String("cosign-issuer", "", "expected OIDC issuer (keyless)")
	sigPath := fs.String("signature", "", "detached cosign signature file")
	certPath := fs.String("certificate", "", "cosign signing certificate (keyless)")
	bundlePath := fs.String("bundle", "", "cosign sigstore bundle")
	fs.Usage = func() {
		fmt.Fprintln(a.Out, "rollops plugin install <path|https-url> [flags]\n\nFetch a plugin binary, verify it, install it, and print the sha256 to pin.")
		fs.PrintDefaults()
	}
	// Source is the first positional; flags follow it (stdlib flag stops at the
	// first positional, so we split before parsing).
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
			fs.Usage()
			return nil
		}
		fs.Usage()
		return fmt.Errorf("plugin install: a source (path or https URL) is required as the first argument")
	}
	source := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("plugin install: unexpected extra arguments: %v", fs.Args())
	}

	// A source that is neither a local path nor an https URL is treated as a
	// marketplace plugin name: resolve it through the registry to a pinned,
	// platform-specific artifact.
	var registryName, registrySHA string
	if !isPathOrURL(source) {
		idx, err := registry.Fetch(ctx, a.httpClient(), *regURL)
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		art, cos, err := idx.Resolve(source, *version, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		registryName, registrySHA = source, art.SHA256
		source = art.URL
		if cos != nil {
			fmt.Fprintf(a.Out, "registry: %s is published by %s\n", registryName, cos.Identity)
		}
	}

	fetch := a.PluginFetcher
	if fetch == nil {
		fetch = fetchPlugin
	}
	local, cleanup, err := fetch(ctx, source)
	if err != nil {
		return fmt.Errorf("plugin install: fetch: %w", err)
	}
	defer cleanup()

	// Optional cosign verification before the binary is placed.
	v := security.CosignBlobVerifier{
		KeyPath: *keyPath, CertIdentity: *identity, CertOIDCIssuer: *issuer,
		SignaturePath: *sigPath, CertificatePath: *certPath, BundlePath: *bundlePath,
		Run: a.CosignRun,
	}
	if v.Configured() {
		ok, reason, verr := v.VerifyBlob(ctx, local)
		if verr != nil {
			return fmt.Errorf("plugin install: cosign: %w", verr)
		}
		if !ok {
			return fmt.Errorf("plugin install: cosign verification failed: %s", reason)
		}
		fmt.Fprintln(a.Out, "cosign: verified")
	}

	sum, err := sha256File(local)
	if err != nil {
		return err
	}
	// Registry installs are pinned by the curated index: the download must match
	// the published sha256, or it is rejected.
	if registrySHA != "" {
		if !strings.EqualFold(sum, registrySHA) {
			return fmt.Errorf("plugin install: registry pin mismatch for %q: index %s, got %s", registryName, registrySHA, sum)
		}
		fmt.Fprintln(a.Out, "registry: sha256 matches the published pin")
	}

	target := *name
	if target == "" {
		if registryName != "" {
			target = registryName
		} else {
			target = filepath.Base(strings.TrimSuffix(source, "/"))
		}
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}
	dest := filepath.Join(*dir, target)
	if err := copyFileMode(local, dest, 0o755); err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}

	fmt.Fprintf(a.Out, "installed %s\nsha256 %s\n", dest, sum)
	fmt.Fprintf(a.Out, "pin it in your rollout spec, e.g.:\n  featureFlags:\n    plugin: %s\n    sha256: %s\n", dest, sum)
	return nil
}

func defaultPluginDir() string {
	if d := os.Getenv("ROLLOPS_PLUGIN_DIR"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".rollops", "plugins")
	}
	return "rollops-plugins"
}

// isPathOrURL reports whether source is an explicit local path or https URL,
// rather than a marketplace plugin name.
func isPathOrURL(source string) bool {
	if strings.Contains(source, "://") {
		return true
	}
	_, err := os.Stat(source)
	return err == nil
}

// fetchPlugin retrieves a local path or https URL to a temp file.
func fetchPlugin(ctx context.Context, source string) (string, func(), error) {
	if strings.HasPrefix(source, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return "", func() {}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", func() {}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return "", func() {}, fmt.Errorf("download %s: status %d", source, resp.StatusCode)
		}
		f, err := os.CreateTemp("", "rollops-plugin-*")
		if err != nil {
			return "", func() {}, err
		}
		cleanup := func() { os.Remove(f.Name()) }
		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			cleanup()
			return "", func() {}, err
		}
		f.Close()
		return f.Name(), cleanup, nil
	}
	if strings.Contains(source, "://") {
		return "", func() {}, fmt.Errorf("unsupported source scheme (use a local path or https URL): %s", source)
	}
	if _, err := os.Stat(source); err != nil {
		return "", func() {}, err
	}
	return source, func() {}, nil // local file: used in place
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
