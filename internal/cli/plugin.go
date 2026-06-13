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
	"sort"
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
		return fmt.Errorf("plugin: subcommand required (search | info | install | list | update)")
	}
	switch args[0] {
	case "search":
		return a.pluginSearch(ctx, args[1:])
	case "info":
		return a.pluginInfo(ctx, args[1:])
	case "install":
		return a.pluginInstall(ctx, args[1:])
	case "list", "ls":
		return a.pluginList(ctx, args[1:])
	case "update", "upgrade":
		return a.pluginUpdate(ctx, args[1:])
	default:
		return fmt.Errorf("plugin: unknown subcommand %q (try: search, info, install, list, update)", args[0])
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

// pluginInfo prints the full registry detail for one plugin — every published
// version with its per-platform artifacts and pins, plus cosign material — so an
// operator can inspect a plugin before installing it.
func (a *App) pluginInfo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin info", flag.ContinueOnError)
	fs.SetOutput(a.Out)
	regURL := fs.String("registry", registry.URL(), "plugin registry index URL")
	// Name is the first positional; flags follow it (stdlib flag stops at the
	// first positional, so we split before parsing).
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("plugin info: a plugin name is required as the first argument")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("plugin info: unexpected extra arguments: %v", fs.Args())
	}
	idx, err := registry.Fetch(ctx, a.httpClient(), *regURL)
	if err != nil {
		return fmt.Errorf("plugin info: %w", err)
	}
	p, ok := idx.Find(name)
	if !ok {
		return fmt.Errorf("plugin info: plugin %q not found in registry", name)
	}
	fmt.Fprintf(a.Out, "%s\n", p.Name)
	if p.Description != "" {
		fmt.Fprintf(a.Out, "  %s\n", p.Description)
	}
	if p.Homepage != "" {
		fmt.Fprintf(a.Out, "  homepage:     %s\n", p.Homepage)
	}
	fmt.Fprintf(a.Out, "  capabilities: %s\n", strings.Join(p.Capabilities, ", "))
	fmt.Fprintf(a.Out, "  latest:       %s\n", p.Latest)
	for _, ver := range sortedVersions(p) {
		fmt.Fprintf(a.Out, "\n  %s\n", ver)
		v := p.Versions[ver]
		if v.Cosign != nil {
			fmt.Fprintf(a.Out, "    cosign: %s (%s)\n", v.Cosign.Identity, v.Cosign.Issuer)
		}
		tw := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
		for _, art := range v.Artifacts {
			fmt.Fprintf(tw, "    %s/%s\t%s\t%s\n", art.OS, art.Arch, art.SHA256, art.URL)
		}
		tw.Flush()
	}
	return nil
}

// sortedVersions returns a plugin's version keys with latest first, then the
// rest in reverse lexical order (a stable, newest-ish-first ordering without a
// semver dependency).
func sortedVersions(p registry.Plugin) []string {
	vers := make([]string, 0, len(p.Versions))
	for v := range p.Versions {
		if v != p.Latest {
			vers = append(vers, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(vers)))
	if _, ok := p.Versions[p.Latest]; ok {
		return append([]string{p.Latest}, vers...)
	}
	return vers
}

// pluginList shows the plugins installed in the plugin directory with the
// sha256 each one would pin to. Offline by default: it reads the filesystem, not
// the registry, so it matches the pins `plugin install` printed.
func (a *App) pluginList(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(a.Out)
	dir := fs.String("dir", defaultPluginDir(), "plugin directory to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(a.Out, "no plugins installed (%s does not exist)\n", *dir)
			return nil
		}
		return fmt.Errorf("plugin list: %w", err)
	}
	tw := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	var listed int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Only regular, executable files are plugins; skip the rest.
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		sum, err := sha256File(filepath.Join(*dir, e.Name()))
		if err != nil {
			return fmt.Errorf("plugin list: %s: %w", e.Name(), err)
		}
		if listed == 0 {
			fmt.Fprintln(tw, "NAME\tSIZE\tSHA256")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\n", e.Name(), info.Size(), sum)
		listed++
	}
	if listed == 0 {
		fmt.Fprintf(a.Out, "no plugins installed in %s\n", *dir)
		return nil
	}
	return tw.Flush()
}

// pluginUpdate compares installed plugins against the marketplace registry and,
// with --apply, upgrades outdated ones to their latest release. An installed
// binary is identified by matching its sha256 to a published artifact, so a
// plugin renamed at install time is still recognised. Dry-run by default.
func (a *App) pluginUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin update", flag.ContinueOnError)
	fs.SetOutput(a.Out)
	dir := fs.String("dir", defaultPluginDir(), "plugin directory to update")
	regURL := fs.String("registry", registry.URL(), "plugin registry index URL")
	apply := fs.Bool("apply", false, "actually upgrade outdated plugins (default: report only)")
	// An optional plugin file name limits the update to one installed plugin;
	// flags may precede or follow it.
	var only string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		only, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if only == "" && fs.NArg() == 1 {
		only = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("plugin update: unexpected extra arguments: %v", fs.Args())
	}

	idx, err := registry.Fetch(ctx, a.httpClient(), *regURL)
	if err != nil {
		return fmt.Errorf("plugin update: %w", err)
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(a.Out, "no plugins installed (%s does not exist)\n", *dir)
			return nil
		}
		return fmt.Errorf("plugin update: %w", err)
	}

	var outdated int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if only != "" && e.Name() != only {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		path := filepath.Join(*dir, e.Name())
		sum, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("plugin update: %s: %w", e.Name(), err)
		}
		name, ver, ok := idx.FindVersionBySHA(sum, runtime.GOOS, runtime.GOARCH)
		if !ok {
			fmt.Fprintf(a.Out, "%s\tunknown (not from this registry)\n", e.Name())
			continue
		}
		p, _ := idx.Find(name)
		if ver == p.Latest {
			fmt.Fprintf(a.Out, "%s\tup to date (%s %s)\n", e.Name(), name, ver)
			continue
		}
		outdated++
		fmt.Fprintf(a.Out, "%s\toutdated (%s %s -> %s)\n", e.Name(), name, ver, p.Latest)
		if !*apply {
			continue
		}
		art, cos, err := idx.Resolve(name, p.Latest, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fmt.Errorf("plugin update: %s: %w", e.Name(), err)
		}
		newSum, err := a.resolveAndInstall(ctx, name, art, cos, path)
		if err != nil {
			return fmt.Errorf("plugin update: %s: %w", e.Name(), err)
		}
		fmt.Fprintf(a.Out, "upgraded %s to %s\nsha256 %s\n", e.Name(), p.Latest, newSum)
	}

	if outdated > 0 && !*apply {
		fmt.Fprintln(a.Out, "run 'rollops plugin update --apply' to upgrade outdated plugins")
	}
	return nil
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
	// platform-specific artifact, verified and installed through the shared path.
	if !isPathOrURL(source) {
		idx, err := registry.Fetch(ctx, a.httpClient(), *regURL)
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		art, cos, err := idx.Resolve(source, *version, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		target := *name
		if target == "" {
			target = source
		}
		dest := filepath.Join(*dir, target)
		sum, err := a.resolveAndInstall(ctx, source, art, cos, dest)
		if err != nil {
			return fmt.Errorf("plugin install: %w", err)
		}
		a.printInstalledPin(dest, sum)
		return nil
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

	// Optional cosign verification before the binary is placed (manual flags only;
	// signed registry releases verify automatically via resolveAndInstall above).
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
	target := *name
	if target == "" {
		target = filepath.Base(strings.TrimSuffix(source, "/"))
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}
	dest := filepath.Join(*dir, target)
	if err := copyFileMode(local, dest, 0o755); err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}
	a.printInstalledPin(dest, sum)
	return nil
}

// resolveAndInstall downloads a resolved marketplace artifact, auto-verifies its
// cosign signature when the release is signed, enforces the index's sha256 pin,
// and writes it to dest (0755). Shared by `plugin install <name>` and
// `plugin update`, so both enforce identical trust checks.
func (a *App) resolveAndInstall(ctx context.Context, name string, art registry.Artifact, cos *registry.Cosign, dest string) (string, error) {
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	if cos != nil {
		fmt.Fprintf(a.Out, "registry: %s is published by %s\n", name, cos.Identity)
	}

	// A signed release verifies automatically: the index's identity/issuer is the
	// expected signer and the signature material rides alongside the artifact.
	var v *security.CosignBlobVerifier
	if cos != nil && art.Signed() {
		rv := &security.CosignBlobVerifier{CertIdentity: cos.Identity, CertOIDCIssuer: cos.Issuer, Run: a.CosignRun}
		if art.Bundle != "" {
			p, clean, err := a.downloadTemp(ctx, art.Bundle)
			if err != nil {
				return "", fmt.Errorf("fetch cosign bundle: %w", err)
			}
			cleanups = append(cleanups, clean)
			rv.BundlePath = p
		} else {
			p, clean, err := a.downloadTemp(ctx, art.Signature)
			if err != nil {
				return "", fmt.Errorf("fetch cosign signature: %w", err)
			}
			cleanups = append(cleanups, clean)
			rv.SignaturePath = p
			if art.Certificate != "" {
				c, clean, err := a.downloadTemp(ctx, art.Certificate)
				if err != nil {
					return "", fmt.Errorf("fetch cosign certificate: %w", err)
				}
				cleanups = append(cleanups, clean)
				rv.CertificatePath = c
			}
		}
		v = rv
	}

	fetch := a.PluginFetcher
	if fetch == nil {
		fetch = fetchPlugin
	}
	local, cleanup, err := fetch(ctx, art.URL)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer cleanup()

	if v != nil {
		fmt.Fprintln(a.Out, "cosign: verifying against the registry-published signature")
		ok, reason, verr := v.VerifyBlob(ctx, local)
		if verr != nil {
			return "", fmt.Errorf("cosign: %w", verr)
		}
		if !ok {
			return "", fmt.Errorf("cosign verification failed: %s", reason)
		}
		fmt.Fprintln(a.Out, "cosign: verified")
	}

	sum, err := sha256File(local)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, art.SHA256) {
		return "", fmt.Errorf("registry pin mismatch for %q: index %s, got %s", name, art.SHA256, sum)
	}
	fmt.Fprintln(a.Out, "registry: sha256 matches the published pin")

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := copyFileMode(local, dest, 0o755); err != nil {
		return "", err
	}
	return sum, nil
}

func (a *App) printInstalledPin(dest, sum string) {
	fmt.Fprintf(a.Out, "installed %s\nsha256 %s\n", dest, sum)
	// Pin it under the spec block matching the plugin's capability —
	// featureFlags, trafficRouting, analysis, or a plugin target.
	fmt.Fprintf(a.Out, "pin it in the matching rollout spec block, e.g.:\n  <featureFlags|trafficRouting|analysis>:\n    plugin: %s\n    sha256: %s\n", dest, sum)
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

// downloadTemp fetches a URL (or copies a local path) to a temp file via the
// app's HTTP client, returning the path and a cleanup. Used for small cosign
// signature material that rides alongside a registry artifact.
func (a *App) downloadTemp(ctx context.Context, src string) (string, func(), error) {
	noop := func() {}
	if !strings.Contains(src, "://") {
		if _, err := os.Stat(src); err != nil {
			return "", noop, err
		}
		return src, noop, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", noop, err
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", noop, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", noop, fmt.Errorf("download %s: status %d", src, resp.StatusCode)
	}
	f, err := os.CreateTemp("", "rollops-cosign-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		cleanup()
		return "", noop, err
	}
	f.Close()
	return f.Name(), cleanup, nil
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
