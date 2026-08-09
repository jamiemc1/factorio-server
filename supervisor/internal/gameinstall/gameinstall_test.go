package gameinstall

import (
	"archive/tar"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestResolveVersionAcceptsExactVersion(t *testing.T) {
	i := New(Config{})
	// An exact version must not hit the network at all.
	got, err := i.resolveVersion(context.Background(), "2.1.14")
	if err != nil || got != "2.1.14" {
		t.Fatalf("resolveVersion = (%q, %v), want (2.1.14, nil)", got, err)
	}
}

func TestResolveVersionRejectsGarbage(t *testing.T) {
	i := New(Config{})
	if _, err := i.resolveVersion(context.Background(), "../../etc"); err == nil {
		t.Fatal("expected an error for a non-version, non-channel value")
	}
}

func TestStripRootRejectsEscapes(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"factorio/bin/x64/factorio", "bin/x64/factorio", true},
		{"./factorio/data/base/info.json", "data/base/info.json", true},
		{"factorio", "", false},
		{"elsewhere/evil", "", false},
		{"factorio/../../etc/passwd", "", false},
	}
	for _, tc := range tests {
		got, ok := stripRoot(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("stripRoot(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestExtractStripsArchiveRoot(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "factorio-headless_linux_9.9.9.tar.xz")
	writeTarXZ(t, archive, map[string]string{
		"factorio/bin/x64/factorio": "#!/bin/sh\n",
		"factorio/data/base/info":   "base\n",
	})

	i := New(Config{GameDir: dir})
	installDir := filepath.Join(dir, "9.9.9")
	if err := i.extract(archive, installDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Binary(installDir)); err != nil {
		t.Fatalf("binary missing after extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(DataDir(installDir), "base", "info")); err != nil {
		t.Fatalf("data missing after extract: %v", err)
	}
	if _, err := os.Stat(installDir + ".staging"); !os.IsNotExist(err) {
		t.Errorf("staging directory was left behind: %v", err)
	}
}

// TestEnsureUsesExistingInstall is the warm-boot case the volume exists for: an
// already-installed version must not produce any HTTP traffic.
func TestEnsureUsesExistingInstall(t *testing.T) {
	dir := t.TempDir()
	installDir := filepath.Join(dir, "2.1.14")
	if err := os.MkdirAll(filepath.Dir(Binary(installDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Binary(installDir), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale older install should be pruned.
	if err := os.MkdirAll(filepath.Join(dir, "2.0.77"), 0o755); err != nil {
		t.Fatal(err)
	}

	i := New(Config{GameDir: dir})
	i.http = &http.Client{Transport: refusingTransport{t}}

	got, err := i.Ensure(context.Background(), "2.1.14")
	if err != nil {
		t.Fatal(err)
	}
	if got != installDir {
		t.Errorf("install dir = %q, want %q", got, installDir)
	}
	if dest, err := os.Readlink(filepath.Join(dir, "current")); err != nil || dest != "2.1.14" {
		t.Errorf("current -> (%q, %v), want 2.1.14", dest, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2.0.77")); !os.IsNotExist(err) {
		t.Errorf("stale install survived pruning: %v", err)
	}
}

func TestResolveVersionFromChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]map[string]string{
			"experimental": {"headless": "2.1.14"},
			"stable":       {"headless": "2.0.77"},
		})
	}))
	defer srv.Close()

	i := New(Config{})
	body, err := i.get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var releases map[string]map[string]string
	if err := json.NewDecoder(body).Decode(&releases); err != nil {
		t.Fatal(err)
	}
	if got := releases["experimental"]["headless"]; got != "2.1.14" {
		t.Errorf("experimental headless = %q, want 2.1.14", got)
	}
}

type refusingTransport struct{ t *testing.T }

func (r refusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected HTTP request to %s on a warm install", req.URL)
	return nil, http.ErrUseLastResponse
}

func writeTarXZ(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	xw, err := xz.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(xw)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
}
