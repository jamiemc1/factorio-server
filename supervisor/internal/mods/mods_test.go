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
	names, err := m.copyPrivateMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "space-exploration" {
		t.Fatalf("internal names = %v, want [space-exploration]", names)
	}
	if _, err := os.Stat(filepath.Join(modsDir, "space-exploration_0.7.58.zip")); err != nil {
		t.Fatalf("mod not copied: %v", err)
	}
}
