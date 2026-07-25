// Package mods assembles /factorio/mods from two sources — private zips vendored
// in the image and public mods resolved to their latest release from the
// Factorio mod portal (cached in S3) — then writes mod-list.json. Behaviour and
// object keys match the previous mod_manager.py.
package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thomasalbrighton/factorio-supervisor/internal/s3"
)

const (
	portalAPI  = "https://mods.factorio.com/api/mods"
	portalBase = "https://mods.factorio.com"
	// The portal sits behind Cloudflare, which 403s the default Go User-Agent;
	// any explicit UA gets through.
	userAgent = "factorio-server-mod-manager/1.0"
)

// builtinMods ship with the base game / DLC. They are enabled in mod-list.json
// but never downloaded (the portal has no zip for them).
var builtinMods = map[string]bool{
	"base": true, "space-age": true, "quality": true, "elevated-rails": true,
}

// Manager performs mod synchronisation.
type Manager struct {
	store      *s3.Store // may be nil if no public mods need downloading
	modsDir    string
	privateDir string
	publicJSON string
	username   string
	token      string
	http       *http.Client
	logf       func(string, ...any)
}

// Config carries the paths and credentials the manager needs.
type Config struct {
	ModsDir    string
	PrivateDir string
	PublicJSON string
	Username   string
	Token      string
	Store      *s3.Store
	Log        func(string, ...any)
}

// New builds a mod Manager.
func New(cfg Config) *Manager {
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{
		store:      cfg.Store,
		modsDir:    cfg.ModsDir,
		privateDir: cfg.PrivateDir,
		publicJSON: cfg.PublicJSON,
		username:   cfg.Username,
		token:      cfg.Token,
		http:       &http.Client{Timeout: 5 * time.Minute},
		logf:       logf,
	}
}

// Sync assembles the mods directory and writes mod-list.json.
func (m *Manager) Sync(ctx context.Context) error {
	publicNames, err := m.loadPublicMods()
	if err != nil {
		return err
	}
	if err := m.cleanModsDir(); err != nil {
		return err
	}

	var enabled []string

	// Split built-ins (enabled, never downloaded) from downloadable public mods.
	var downloadable []string
	for _, n := range publicNames {
		if builtinMods[n] {
			enabled = append(enabled, n)
		} else {
			downloadable = append(downloadable, n)
		}
	}

	if len(downloadable) > 0 {
		if m.store == nil {
			return fmt.Errorf("public mods listed but S3 cache is not configured")
		}
		if m.username == "" || m.token == "" {
			return fmt.Errorf("public mods listed but Factorio portal credentials are missing")
		}
		if err := m.syncPublicMods(ctx, downloadable); err != nil {
			return err
		}
		// Keep public.json order in the enabled list so mod-list.json is stable.
		enabled = append(enabled, downloadable...)
	}

	privateNames, err := m.copyPrivateMods()
	if err != nil {
		return err
	}
	enabled = append(enabled, privateNames...)

	if err := m.writeModList(enabled); err != nil {
		return err
	}
	m.logf("Mod sync complete")
	return nil
}

// syncModWorkers is the number of public mods downloaded/cached concurrently.
const syncModWorkers = 8

// syncPublicMods resolves and caches every downloadable public mod using a pool
// of worker goroutines. It returns the first error encountered; on error the
// shared context is cancelled so in-flight downloads stop promptly.
func (m *Manager) syncPublicMods(ctx context.Context, names []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan string)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

	workers := min(len(names), syncModWorkers)
	for range workers {
		wg.Go(func() {
			for name := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := m.syncPublicMod(ctx, name); err != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("sync %s: %w", name, err)
						cancel()
					})
					return
				}
			}
		})
	}

	// Feed jobs, stopping early if a worker has already failed.
	for _, name := range names {
		select {
		case jobs <- name:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()

	return firstErr
}

type publicModsFile struct {
	Mods []string `json:"mods"`
}

func (m *Manager) loadPublicMods() ([]string, error) {
	data, err := os.ReadFile(m.publicJSON)
	if err != nil {
		if os.IsNotExist(err) {
			m.logf("No public mod list at %s, skipping public mods", m.publicJSON)
			return nil, nil
		}
		return nil, err
	}
	var f publicModsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.publicJSON, err)
	}
	return f.Mods, nil
}

func (m *Manager) cleanModsDir() error {
	if err := os.MkdirAll(m.modsDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.modsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zip") {
			if err := os.Remove(filepath.Join(m.modsDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

type portalRelease struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	FileName    string `json:"file_name"`
}

type portalFull struct {
	Releases []portalRelease `json:"releases"`
}

func (m *Manager) portalReleases(ctx context.Context, name string) ([]portalRelease, error) {
	u := fmt.Sprintf("%s/%s/full", portalAPI, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("portal returned %s for %s", resp.Status, name)
	}
	var full portalFull
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, err
	}
	return full.Releases, nil
}

// syncPublicMod ensures the latest release of one public mod is present in the
// mods dir, using the S3 cache when possible and populating it on a miss.
func (m *Manager) syncPublicMod(ctx context.Context, name string) error {
	releases, err := m.portalReleases(ctx, name)
	if err != nil {
		return err
	}
	if len(releases) == 0 {
		return fmt.Errorf("%s has no releases on the portal", name)
	}
	// The portal lists releases chronologically; the last is the latest.
	release := releases[len(releases)-1]
	version := release.Version
	filename := fmt.Sprintf("%s_%s.zip", name, version)
	localPath := filepath.Join(m.modsDir, filename)
	cacheKey := "mods/" + filename

	exists, err := m.store.Exists(ctx, cacheKey)
	if err != nil {
		return fmt.Errorf("checking cache for %s: %w", name, err)
	}
	if exists {
		m.logf("Cache hit: %s latest=%s <- s3://%s", name, version, cacheKey)
		return m.store.DownloadFile(ctx, cacheKey, localPath)
	}

	m.logf("Cache miss: %s latest=%s, fetching from portal", name, version)
	if err := m.downloadFromPortal(ctx, release, localPath); err != nil {
		return err
	}
	m.logf("Caching to s3://%s", cacheKey)
	return m.store.UploadFile(ctx, localPath, cacheKey)
}

func (m *Manager) downloadFromPortal(ctx context.Context, release portalRelease, destPath string) error {
	q := url.Values{"username": {m.username}, "token": {m.token}}
	u := fmt.Sprintf("%s%s?%s", portalBase, release.DownloadURL, q.Encode())
	m.logf("Downloading from portal: %s", release.DownloadURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("portal download returned %s", resp.Status)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// copyPrivateMods copies vendored private zips into the mods dir and returns
// their internal names (filename minus the trailing _<version>.zip).
func (m *Manager) copyPrivateMods() ([]string, error) {
	info, err := os.Stat(m.privateDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(m.privateDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	files := make([]string, 0)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zip") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, fname := range files {
		src := filepath.Join(m.privateDir, fname)
		dst := filepath.Join(m.modsDir, fname)
		if err := copyFile(src, dst); err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(fname, ".zip")
		internal := stem
		if i := strings.LastIndex(stem, "_"); i != -1 {
			internal = stem[:i]
		}
		names = append(names, internal)
		m.logf("Copied private mod: %s (name=%s)", fname, internal)
	}
	return names, nil
}

type modListEntry struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type modList struct {
	Mods []modListEntry `json:"mods"`
}

// writeModList writes mod-list.json enabling base + every supplied name, de-duped
// and with base first.
func (m *Manager) writeModList(enabledNames []string) error {
	seen := map[string]bool{}
	var ordered []string
	for _, name := range append([]string{"base"}, enabledNames...) {
		if !seen[name] {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}
	ml := modList{Mods: make([]modListEntry, 0, len(ordered))}
	for _, name := range ordered {
		ml.Mods = append(ml.Mods, modListEntry{Name: name, Enabled: true})
	}
	data, err := json.MarshalIndent(ml, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.modsDir, "mod-list.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	m.logf("Wrote %s with %d mods enabled: %s", path, len(ordered), strings.Join(ordered, ", "))
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
