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
	"slices"
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

// dlcMods are the Space Age expansion mods that DLC_SPACE_AGE toggles. Any of
// these not selected gets an explicit enabled:false entry in mod-list.json,
// reproducing what the base image's docker-dlc.sh used to do.
var dlcMods = []string{"elevated-rails", "quality", "space-age", "recycler"}

// Manager performs mod synchronisation.
type Manager struct {
	store      *s3.Store // may be nil if no public mods need downloading
	modsDir    string
	privateDir string
	publicJSON string
	username   string
	token      string
	dlc        string
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
	// DLCSpaceAge is the DLC_SPACE_AGE value: "true", "false", or a
	// space-separated list of expansion mods to enable.
	DLCSpaceAge string
	Store       *s3.Store
	Log         func(string, ...any)
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
		dlc:        cfg.DLCSpaceAge,
		http:       &http.Client{Timeout: 5 * time.Minute},
		logf:       logf,
	}
}

// Sync reconciles the mods directory against public.json and writes
// mod-list.json.
//
// The mods directory lives on the persistent volume, so it is itself the
// durable cache: only zips that are absent are fetched, and only zips that are
// no longer wanted are deleted. On a warm boot this touches neither the portal
// (beyond cheap release metadata) nor S3.
func (m *Manager) Sync(ctx context.Context) error {
	publicNames, err := m.loadPublicMods()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.modsDir, 0o755); err != nil {
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

	var releases map[string]portalRelease
	if len(downloadable) > 0 {
		if m.store == nil {
			return fmt.Errorf("public mods listed but S3 cache is not configured")
		}
		if m.username == "" || m.token == "" {
			return fmt.Errorf("public mods listed but Factorio portal credentials are missing")
		}
		if releases, err = m.resolveReleases(ctx, downloadable); err != nil {
			return err
		}
	}

	// Everything the mods dir should contain once we are done: the resolved
	// public releases plus the vendored private zips.
	wanted := make(map[string]bool, len(releases))
	for _, r := range releases {
		wanted[r.FileName] = true
	}
	privateFiles, privateNames, err := m.privateMods()
	if err != nil {
		return err
	}
	for _, f := range privateFiles {
		wanted[f] = true
	}

	if err := m.pruneModsDir(wanted); err != nil {
		return err
	}
	if len(downloadable) > 0 {
		if err := m.fetchMissing(ctx, downloadable, releases); err != nil {
			return err
		}
		// Keep public.json order in the enabled list so mod-list.json is stable.
		enabled = append(enabled, downloadable...)
	}
	if err := m.copyPrivateMods(privateFiles); err != nil {
		return err
	}
	enabled = append(enabled, privateNames...)

	if err := m.writeModList(enabled); err != nil {
		return err
	}
	m.logf("Mod sync complete")
	return nil
}

// syncModWorkers is the number of public mods resolved/downloaded concurrently.
const syncModWorkers = 8

// resolveReleases looks up the latest release of every named mod concurrently.
func (m *Manager) resolveReleases(ctx context.Context, names []string) (map[string]portalRelease, error) {
	var mu sync.Mutex
	out := make(map[string]portalRelease, len(names))
	err := m.runWorkers(ctx, names, func(ctx context.Context, name string) error {
		release, err := m.latestRelease(ctx, name)
		if err != nil {
			return err
		}
		mu.Lock()
		out[name] = release
		mu.Unlock()
		return nil
	})
	return out, err
}

// fetchMissing downloads every resolved release that is not already on disk.
func (m *Manager) fetchMissing(ctx context.Context, names []string, releases map[string]portalRelease) error {
	var missing []string
	for _, name := range names {
		release, ok := releases[name]
		if !ok {
			return fmt.Errorf("no resolved release for %s", name)
		}
		if _, err := os.Stat(filepath.Join(m.modsDir, release.FileName)); err == nil {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) == 0 {
		m.logf("All %d public mods already present on disk", len(names))
		return nil
	}
	m.logf("Fetching %d of %d public mods", len(missing), len(names))
	return m.runWorkers(ctx, missing, func(ctx context.Context, name string) error {
		return m.fetchMod(ctx, name, releases[name])
	})
}

// runWorkers applies fn to every item using a bounded pool. It returns the first
// error encountered; on error the shared context is cancelled so in-flight work
// stops promptly.
func (m *Manager) runWorkers(ctx context.Context, items []string, fn func(context.Context, string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan string)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

	workers := min(len(items), syncModWorkers)
	for range workers {
		wg.Go(func() {
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, item); err != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("sync %s: %w", item, err)
						cancel()
					})
					return
				}
			}
		})
	}

	// Feed jobs, stopping early if a worker has already failed.
	for _, item := range items {
		select {
		case jobs <- item:
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

// pruneModsDir removes zips the current mod set no longer wants — superseded
// versions and mods dropped from public.json — leaving wanted files in place.
func (m *Manager) pruneModsDir(wanted map[string]bool) error {
	entries, err := os.ReadDir(m.modsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".zip") || wanted[name] {
			continue
		}
		m.logf("Removing stale mod %s", name)
		if err := os.Remove(filepath.Join(m.modsDir, name)); err != nil {
			return err
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

// latestRelease resolves one mod's newest release, normalising FileName so the
// rest of the package can rely on it.
func (m *Manager) latestRelease(ctx context.Context, name string) (portalRelease, error) {
	releases, err := m.portalReleases(ctx, name)
	if err != nil {
		return portalRelease{}, err
	}
	if len(releases) == 0 {
		return portalRelease{}, fmt.Errorf("%s has no releases on the portal", name)
	}
	// The portal lists releases chronologically; the last is the latest.
	release := releases[len(releases)-1]
	release.FileName = fmt.Sprintf("%s_%s.zip", name, release.Version)
	return release, nil
}

// fetchMod puts one release's zip in the mods dir, using the S3 cache when
// possible and populating it on a miss.
func (m *Manager) fetchMod(ctx context.Context, name string, release portalRelease) error {
	localPath := filepath.Join(m.modsDir, release.FileName)
	cacheKey := "mods/" + release.FileName

	exists, err := m.store.Exists(ctx, cacheKey)
	if err != nil {
		return fmt.Errorf("checking cache for %s: %w", name, err)
	}
	if exists {
		m.logf("Cache hit: %s latest=%s <- s3://%s", name, release.Version, cacheKey)
		return m.store.DownloadFile(ctx, cacheKey, localPath)
	}

	m.logf("Cache miss: %s latest=%s, fetching from portal", name, release.Version)
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

// privateMods lists the vendored private zips: their filenames and their
// internal mod names (filename minus the trailing _<version>.zip).
func (m *Manager) privateMods() (files, names []string, err error) {
	info, err := os.Stat(m.privateDir)
	if err != nil || !info.IsDir() {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(m.privateDir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zip") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, fname := range files {
		stem := strings.TrimSuffix(fname, ".zip")
		internal := stem
		if i := strings.LastIndex(stem, "_"); i != -1 {
			internal = stem[:i]
		}
		names = append(names, internal)
	}
	return files, names, nil
}

// copyPrivateMods copies vendored private zips into the mods dir, skipping any
// that are already there at the same size.
func (m *Manager) copyPrivateMods(files []string) error {
	for _, fname := range files {
		src := filepath.Join(m.privateDir, fname)
		dst := filepath.Join(m.modsDir, fname)
		if sameSize(src, dst) {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
		m.logf("Copied private mod: %s", fname)
	}
	return nil
}

// sameSize reports whether both paths exist with an identical size — enough to
// tell an already-copied mod zip from a changed one.
func sameSize(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return ai.Size() == bi.Size()
}

// selectedDLC parses DLC_SPACE_AGE into the set of expansion mods to enable.
// It accepts "true" (all), "false" (none), or a space-separated list, matching
// the base image's docker-dlc.sh.
func selectedDLC(value string) map[string]bool {
	enabled := map[string]bool{}
	switch strings.TrimSpace(value) {
	case "true", "":
		// An unset DLC_SPACE_AGE means the base image's default of "true".
		for _, name := range dlcMods {
			enabled[name] = true
		}
	case "false":
	default:
		for _, name := range strings.Fields(value) {
			enabled[name] = true
		}
	}
	return enabled
}

type modListEntry struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type modList struct {
	Mods []modListEntry `json:"mods"`
}

// writeModList writes mod-list.json enabling base + every supplied name, de-duped
// and with base first. Expansion mods that DLC_SPACE_AGE does not select are
// written with enabled:false, which is what disabling a DLC means to Factorio.
func (m *Manager) writeModList(enabledNames []string) error {
	seen := map[string]bool{}
	var ordered []string
	for _, name := range append([]string{"base"}, enabledNames...) {
		if !seen[name] {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}

	dlcEnabled := selectedDLC(m.dlc)
	for _, name := range dlcMods {
		if !dlcEnabled[name] && !seen[name] {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}

	ml := modList{Mods: make([]modListEntry, 0, len(ordered))}
	var disabled []string
	for _, name := range ordered {
		enabled := true
		if slices.Contains(dlcMods, name) && !dlcEnabled[name] {
			enabled = false
			disabled = append(disabled, name)
		}
		ml.Mods = append(ml.Mods, modListEntry{Name: name, Enabled: enabled})
	}
	if len(disabled) > 0 {
		m.logf("DLC_SPACE_AGE=%q disables: %s", m.dlc, strings.Join(disabled, ", "))
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
