package configwatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherReloadsWhenConfigFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("routes: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Start(ctx, path, 20*time.Millisecond, func(got string) error {
		reloaded <- got
		return nil
	}, nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if err := os.WriteFile(path, []byte("routes:\n  - serverAddress: changed.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForReload(t, reloaded, path)
}

func TestWatcherReloadsWhenConfigMapDataSymlinkChanges(t *testing.T) {
	dir := t.TempDir()
	writeVersionedConfig(t, dir, "version-one", "routes: []\n")
	if err := os.Symlink("version-one", filepath.Join(dir, "..data")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join("..data", "config.yaml"), path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	reloaded := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Start(ctx, path, 20*time.Millisecond, func(got string) error {
		reloaded <- got
		return nil
	}, nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	writeVersionedConfig(t, dir, "version-two", "routes:\n  - serverAddress: updated.example.com\n")
	temporaryDataLink := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink("version-two", temporaryDataLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryDataLink, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	waitForReload(t, reloaded, path)
}

func TestStartRejectsInvalidArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("routes: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), path, 0, func(string) error { return nil }, nil); err == nil {
		t.Fatal("Start succeeded with a zero debounce")
	}
	if _, err := Start(context.Background(), path, time.Second, nil, nil); err == nil {
		t.Fatal("Start succeeded with a nil reload function")
	}
}

func writeVersionedConfig(t *testing.T, dir, version, contents string) {
	t.Helper()
	versionDir := filepath.Join(dir, version)
	if err := os.Mkdir(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForReload(t *testing.T, reloaded <-chan string, wantPath string) {
	t.Helper()
	select {
	case got := <-reloaded:
		if got != wantPath {
			t.Fatalf("reload path = %q, want %q", got, wantPath)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config reload")
	}
}
