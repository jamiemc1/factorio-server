package s3

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// SaveManager encapsulates the save up/download policy, keeping object keys and
// behaviour byte-identical to the previous save_file_manager.py.
type SaveManager struct {
	store    *Store
	savesDir string
	saveName string
	logf     func(string, ...any)
}

// NewSaveManager builds a save manager. savesDir is typically /factorio/saves.
func NewSaveManager(store *Store, savesDir, saveName string, logf func(string, ...any)) *SaveManager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &SaveManager{store: store, savesDir: savesDir, saveName: saveName, logf: logf}
}

func (m *SaveManager) savePath(name string) string {
	return filepath.Join(m.savesDir, name+".zip")
}

// Upload pushes the canonical save, a timestamped backup, and _autosave1 (if
// distinct) to the bucket. It returns an error if any upload fails, matching the
// Python exit-code-1 behaviour.
func (m *SaveManager) Upload(ctx context.Context) error {
	m.logf("Using save_name=%s", m.saveName)
	var failed bool

	source := m.savePath(m.saveName)
	destKey := m.saveName + ".zip"
	m.logf("Uploading %s -> s3://%s", source, destKey)
	if err := m.uploadOne(ctx, source, destKey); err != nil {
		m.logf("FAILED: upload %s: %v", destKey, err)
		failed = true
	} else {
		m.logf("Uploaded %s", destKey)
	}

	tsKey := fmt.Sprintf("%s_%s.zip", m.saveName, TimestampSuffix())
	m.logf("Uploading timestamped backup -> s3://%s", tsKey)
	if err := m.uploadOne(ctx, source, tsKey); err != nil {
		m.logf("FAILED: upload %s: %v", tsKey, err)
		failed = true
	} else {
		m.logf("Uploaded %s", tsKey)
	}

	// Always ensure _autosave1 is captured, even when SAVE_NAME differs.
	autosaveSrc := filepath.Join(m.savesDir, "_autosave1.zip")
	if autosaveSrc != source {
		autosaveKey := "_autosave1.zip"
		m.logf("Uploading %s -> s3://%s", autosaveSrc, autosaveKey)
		if err := m.uploadOne(ctx, autosaveSrc, autosaveKey); err != nil {
			m.logf("FAILED: upload %s: %v", autosaveKey, err)
			failed = true
		} else {
			m.logf("Uploaded %s", autosaveKey)
		}
	}

	if failed {
		return fmt.Errorf("one or more save uploads failed")
	}
	m.logf("All uploads completed successfully")
	return nil
}

func (m *SaveManager) uploadOne(ctx context.Context, localPath, key string) error {
	if _, err := os.Stat(localPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source file not found: %s", localPath)
		}
		return err
	}
	return m.store.UploadFile(ctx, localPath, key)
}

// Download pulls the canonical save and _autosave1 from the bucket into
// savesDir. A missing canonical save is logged but not fatal (a fresh world);
// this mirrors the Python behaviour.
func (m *SaveManager) Download(ctx context.Context) error {
	srcKey := m.saveName + ".zip"
	dest := m.savePath(m.saveName)
	m.logf("Downloading s3://%s -> %s", srcKey, dest)
	if err := m.store.DownloadFile(ctx, srcKey, dest); err != nil {
		if IsNotFound(err) {
			m.logf("FAILED: source not found in S3: %s", srcKey)
		} else {
			return err
		}
	} else {
		m.logf("Downloaded %s", dest)
	}

	autosaveKey := "_autosave1.zip"
	autosaveDest := filepath.Join(m.savesDir, "_autosave1.zip")
	m.logf("Downloading s3://%s -> %s", autosaveKey, autosaveDest)
	if err := m.store.DownloadFile(ctx, autosaveKey, autosaveDest); err != nil {
		if IsNotFound(err) {
			m.logf("FAILED: source not found in S3: %s", autosaveKey)
		} else {
			return err
		}
	} else {
		m.logf("Downloaded %s", autosaveDest)
	}
	return nil
}

// DownloadIfNewer pulls each save from the bucket only when the local copy is
// missing or older than S3's. With a persistent volume the local save is
// normally the freshest copy — it is what the last upload came from — so this
// keeps warm boots free of save traffic while still recovering the world after
// a volume loss or an out-of-band restore.
func (m *SaveManager) DownloadIfNewer(ctx context.Context) error {
	targets := []struct{ key, dest string }{
		{m.saveName + ".zip", m.savePath(m.saveName)},
	}
	if m.saveName != "_autosave1" {
		targets = append(targets, struct{ key, dest string }{"_autosave1.zip", filepath.Join(m.savesDir, "_autosave1.zip")})
	}

	for _, t := range targets {
		_, remoteMtime, err := m.store.Stat(ctx, t.key)
		if err != nil {
			if IsNotFound(err) {
				m.logf("Not in S3, skipping: %s", t.key)
				continue
			}
			return err
		}
		if info, err := os.Stat(t.dest); err == nil {
			if !remoteMtime.After(info.ModTime()) {
				m.logf("Local %s is current (S3 copy is not newer) — keeping it", filepath.Base(t.dest))
				continue
			}
			m.logf("S3 copy of %s is newer than the local save — downloading", t.key)
		} else if !os.IsNotExist(err) {
			return err
		}
		m.logf("Downloading s3://%s -> %s", t.key, t.dest)
		if err := m.store.DownloadFile(ctx, t.key, t.dest); err != nil {
			return err
		}
		m.logf("Downloaded %s", t.dest)
	}
	return nil
}

// UploadIfChanged uploads only the canonical save when its mtime differs from
// lastMtime, returning the new mtime to remember. Used by the periodic monitor.
func (m *SaveManager) UploadIfChanged(ctx context.Context, lastMtime int64) (int64, bool, error) {
	source := m.savePath(m.saveName)
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return lastMtime, false, nil
		}
		return lastMtime, false, err
	}
	mtime := info.ModTime().UnixNano()
	if mtime == lastMtime {
		return lastMtime, false, nil
	}
	destKey := m.saveName + ".zip"
	if err := m.store.UploadFile(ctx, source, destKey); err != nil {
		return lastMtime, false, err
	}
	m.logf("Periodic save upload: %s", destKey)
	return mtime, true, nil
}
