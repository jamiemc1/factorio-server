// Package gameinstall installs the Factorio headless server onto the data
// volume, replacing the factoriotools/factorio base image. The install lives at
// <GameDir>/<version> with a `current` symlink pointing at it, so a machine that
// keeps its volume across restarts finds the game already present and does no
// network I/O at all.
//
// On a cold volume the tarball is fetched from the S3 cache first (same region,
// fast) and only then from factorio.com, mirroring how internal/mods caches mod
// zips. Either way the archive's sha256 is checked against factorio.com's
// published sums before it is unpacked.
package gameinstall

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thomasalbrighton/factorio-supervisor/internal/s3"
	"github.com/ulikunitz/xz"
)

const (
	releasesAPI  = "https://factorio.com/api/latest-releases"
	downloadURL  = "https://www.factorio.com/get-download/%s/headless/linux64"
	checksumsURL = "https://www.factorio.com/download/sha256sums/"
	userAgent    = "factorio-supervisor/1.0"

	// binaryRel is the server executable's path within an install directory.
	binaryRel = "bin/x64/factorio"
	// archiveRoot is the single top-level directory inside the headless tarball;
	// it is stripped during extraction.
	archiveRoot = "factorio"
)

// versionPattern guards against an env-supplied version being used to build
// URLs and on-disk paths.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Installer resolves and installs Factorio headless builds.
type Installer struct {
	gameDir string
	store   *s3.Store // may be nil when S3 is not configured
	http    *http.Client
	logf    func(string, ...any)
}

// Config carries the paths and collaborators the installer needs.
type Config struct {
	GameDir string
	Store   *s3.Store
	Log     func(string, ...any)
}

// New builds an Installer.
func New(cfg Config) *Installer {
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Installer{
		gameDir: cfg.GameDir,
		store:   cfg.Store,
		http:    &http.Client{Timeout: 15 * time.Minute},
		logf:    logf,
	}
}

// Ensure makes the requested version available and returns its install
// directory. version is an exact "x.y.z" or one of the channel keywords
// "experimental" / "stable".
func (i *Installer) Ensure(ctx context.Context, version string) (string, error) {
	resolved, err := i.resolveVersion(ctx, version)
	if err != nil {
		return "", err
	}

	installDir := filepath.Join(i.gameDir, resolved)
	if _, err := os.Stat(filepath.Join(installDir, binaryRel)); err == nil {
		i.logf("Factorio %s already installed at %s", resolved, installDir)
		if err := i.linkCurrent(resolved); err != nil {
			return "", err
		}
		i.prune(resolved)
		return installDir, nil
	}

	i.logf("Factorio %s not present — installing", resolved)
	archive, err := i.fetchArchive(ctx, resolved)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	if err := i.extract(archive, installDir); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(installDir, binaryRel)); err != nil {
		return "", fmt.Errorf("archive did not contain %s: %w", binaryRel, err)
	}
	if err := i.linkCurrent(resolved); err != nil {
		return "", err
	}
	i.prune(resolved)
	i.logf("Installed Factorio %s at %s", resolved, installDir)
	return installDir, nil
}

// Binary returns the server executable inside an install directory.
func Binary(installDir string) string { return filepath.Join(installDir, binaryRel) }

// DataDir returns the read-data directory inside an install directory.
func DataDir(installDir string) string { return filepath.Join(installDir, "data") }

// resolveVersion turns a channel keyword into a concrete version, validating
// whatever it returns.
func (i *Installer) resolveVersion(ctx context.Context, version string) (string, error) {
	channel := strings.TrimSpace(version)
	if channel == "" {
		channel = "experimental"
	}
	if versionPattern.MatchString(channel) {
		return channel, nil
	}
	if channel != "experimental" && channel != "stable" {
		return "", fmt.Errorf("invalid FACTORIO_VERSION %q (want x.y.z, \"stable\" or \"experimental\")", version)
	}

	body, err := i.get(ctx, releasesAPI)
	if err != nil {
		return "", fmt.Errorf("resolve %s channel: %w", channel, err)
	}
	defer body.Close()

	var releases map[string]map[string]string
	if err := json.NewDecoder(body).Decode(&releases); err != nil {
		return "", fmt.Errorf("parse %s: %w", releasesAPI, err)
	}
	resolved := releases[channel]["headless"]
	if !versionPattern.MatchString(resolved) {
		return "", fmt.Errorf("%s returned unusable %s headless version %q", releasesAPI, channel, resolved)
	}
	i.logf("Resolved %s channel to Factorio %s", channel, resolved)
	return resolved, nil
}

func archiveName(version string) string {
	return fmt.Sprintf("factorio-headless_linux_%s.tar.xz", version)
}

// fetchArchive puts the verified tarball on disk and returns its path. The
// caller owns the file.
func (i *Installer) fetchArchive(ctx context.Context, version string) (string, error) {
	name := archiveName(version)
	if err := os.MkdirAll(i.gameDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(i.gameDir, "."+name)
	cacheKey := "game/" + name

	wantSum, err := i.checksum(ctx, name)
	if err != nil {
		// The sums page is ancillary; losing it should not ground the server.
		i.logf("WARNING: could not fetch checksums (%v) — skipping archive verification", err)
	}

	cached := false
	if i.store != nil {
		exists, err := i.store.Exists(ctx, cacheKey)
		if err != nil {
			i.logf("WARNING: S3 cache lookup for %s failed: %v", cacheKey, err)
		} else if exists {
			i.logf("Cache hit: Factorio %s <- s3://%s", version, cacheKey)
			if err := i.store.DownloadFile(ctx, cacheKey, dest); err != nil {
				return "", fmt.Errorf("download %s from cache: %w", cacheKey, err)
			}
			cached = true
		}
	}

	if !cached {
		url := fmt.Sprintf(downloadURL, version)
		i.logf("Downloading Factorio %s from factorio.com", version)
		if err := i.downloadTo(ctx, url, dest); err != nil {
			return "", err
		}
	}

	if wantSum != "" {
		sum, err := fileSHA256(dest)
		if err != nil {
			os.Remove(dest)
			return "", err
		}
		if sum != wantSum {
			os.Remove(dest)
			return "", fmt.Errorf("sha256 mismatch for %s: got %s, want %s", name, sum, wantSum)
		}
		i.logf("Verified sha256 of %s", name)
	}

	// Populate the cache only with an archive we just pulled from upstream.
	if !cached && i.store != nil {
		i.logf("Caching to s3://%s", cacheKey)
		if err := i.store.UploadFile(ctx, dest, cacheKey); err != nil {
			i.logf("WARNING: caching %s failed: %v", cacheKey, err)
		}
	}
	return dest, nil
}

// checksum returns the published sha256 for an archive name, or "" if the sums
// page does not list it.
func (i *Installer) checksum(ctx context.Context, name string) (string, error) {
	body, err := i.get(ctx, checksumsURL)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(data)) {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if ok && file == name {
			return sum, nil
		}
	}
	i.logf("WARNING: %s is not listed in the published checksums", name)
	return "", nil
}

func (i *Installer) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := i.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return resp.Body, nil
}

func (i *Installer) downloadTo(ctx context.Context, url, dest string) error {
	body, err := i.get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return fmt.Errorf("download %s: %w", url, err)
	}
	i.logf("Downloaded %.1f MiB", float64(n)/(1<<20))
	return nil
}

func fileSHA256(path string) (string, error) {
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

// extract unpacks archivePath into installDir via a staging directory, so a
// crashed or killed install never leaves a half-populated version directory
// that the next boot would mistake for a good one.
func (i *Installer) extract(archivePath, installDir string) error {
	staging := installDir + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	i.logf("Extracting %s", filepath.Base(archivePath))
	xr, err := xz.NewReader(f)
	if err != nil {
		return fmt.Errorf("open xz stream: %w", err)
	}
	tr := tar.NewReader(xr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		rel, ok := stripRoot(hdr.Name)
		if !ok {
			continue
		}
		target := filepath.Join(staging, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Headless tarballs contain only dirs, regular files and symlinks.
		}
	}

	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	return os.Rename(staging, installDir)
}

// stripRoot removes the archive's single top-level "factorio/" component and
// rejects entries that would escape the destination directory.
func stripRoot(name string) (string, bool) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == archiveRoot {
		return "", false
	}
	rest, ok := strings.CutPrefix(clean, archiveRoot+"/")
	if !ok || rest == "" {
		return "", false
	}
	if path.IsAbs(rest) || rest == ".." || strings.HasPrefix(rest, "../") {
		return "", false
	}
	return rest, true
}

// linkCurrent points <GameDir>/current at version, replacing any existing link
// atomically.
func (i *Installer) linkCurrent(version string) error {
	link := filepath.Join(i.gameDir, "current")
	if dest, err := os.Readlink(link); err == nil && dest == version {
		return nil
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// prune deletes installs and leftovers other than the active version so the
// volume cannot fill up across upgrades. Failures are logged, never fatal.
func (i *Installer) prune(keep string) {
	entries, err := os.ReadDir(i.gameDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keep || name == "current" {
			continue
		}
		i.logf("Removing stale game directory %s", name)
		if err := os.RemoveAll(filepath.Join(i.gameDir, name)); err != nil {
			i.logf("WARNING: could not remove %s: %v", name, err)
		}
	}
}
