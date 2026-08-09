package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteModListDedupesAndPutsBaseFirst(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{ModsDir: dir})

	if err := m.writeModList([]string{"space-age", "base", "flib", "flib"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mod-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ml modList
	if err := json.Unmarshal(data, &ml); err != nil {
		t.Fatal(err)
	}
	want := []string{"base", "space-age", "flib"}
	if len(ml.Mods) != len(want) {
		t.Fatalf("got %d mods, want %d: %+v", len(ml.Mods), len(want), ml.Mods)
	}
	for i, name := range want {
		if ml.Mods[i].Name != name || !ml.Mods[i].Enabled {
			t.Fatalf("mod[%d] = %+v, want {%s true}", i, ml.Mods[i], name)
		}
	}
}

func TestCopyPrivateModsDerivesInternalName(t *testing.T) {
	root := t.TempDir()
	modsDir := filepath.Join(root, "mods")
	privateDir := filepath.Join(root, "private")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Filename with a version suffix -> internal name strips the _<version>.
	if err := os.WriteFile(filepath.Join(privateDir, "space-exploration_0.7.58.zip"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Config{ModsDir: modsDir, PrivateDir: privateDir})
	files, names, err := m.privateMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "space-exploration" {
		t.Fatalf("internal names = %v, want [space-exploration]", names)
	}
	if err := m.copyPrivateMods(files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(modsDir, "space-exploration_0.7.58.zip")); err != nil {
		t.Fatalf("mod not copied: %v", err)
	}
}

func TestPruneModsDirKeepsWantedZips(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"flib_0.16.0.zip", "flib_0.15.0.zip", "mod-list.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(Config{ModsDir: dir})
	if err := m.pruneModsDir(map[string]bool{"flib_0.16.0.zip": true}); err != nil {
		t.Fatal(err)
	}
	// The wanted zip survives — that is what makes the volume a durable cache —
	// while the superseded version is removed and non-zips are left alone.
	if _, err := os.Stat(filepath.Join(dir, "flib_0.16.0.zip")); err != nil {
		t.Errorf("wanted zip was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "flib_0.15.0.zip")); !os.IsNotExist(err) {
		t.Errorf("stale zip survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mod-list.json")); err != nil {
		t.Errorf("non-zip was deleted: %v", err)
	}
}

func TestWriteModListDisablesUnselectedDLC(t *testing.T) {
	tests := []struct {
		dlc  string
		want map[string]bool // mod name -> enabled
	}{
		// The deployed setting: elevated-rails on, the rest explicitly off.
		{"elevated-rails", map[string]bool{"base": true, "elevated-rails": true, "space-age": false, "quality": false}},
		{"true", map[string]bool{"base": true, "elevated-rails": true}},
		{"false", map[string]bool{"base": true, "elevated-rails": false, "space-age": false, "quality": false}},
	}
	for _, tc := range tests {
		t.Run(tc.dlc, func(t *testing.T) {
			dir := t.TempDir()
			m := New(Config{ModsDir: dir, DLCSpaceAge: tc.dlc})
			if err := m.writeModList([]string{"elevated-rails", "flib"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "mod-list.json"))
			if err != nil {
				t.Fatal(err)
			}
			var ml modList
			if err := json.Unmarshal(data, &ml); err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, e := range ml.Mods {
				got[e.Name] = e.Enabled
			}
			for name, want := range tc.want {
				if enabled, ok := got[name]; !ok || enabled != want {
					t.Errorf("%s = (%v, present=%v), want %v", name, enabled, ok, want)
				}
			}
			if !got["flib"] {
				t.Error("non-DLC mod flib should stay enabled")
			}
		})
	}
}
