package kubernetes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNamespaceExplicitReturnsConfiguredNamespace(t *testing.T) {
	got, err := ResolveNamespace("minecraft", NamespaceResolver{})
	if err != nil {
		t.Fatalf("ResolveNamespace returned error: %v", err)
	}
	if got != "minecraft" {
		t.Fatalf("namespace = %q, want %q", got, "minecraft")
	}
}

func TestResolveNamespaceExplicitDoesNotReadFile(t *testing.T) {
	resolver := NamespaceResolver{
		Path: filepath.Join(t.TempDir(), "namespace"),
		ReadFile: func(string) ([]byte, error) {
			t.Fatal("ReadFile called for explicit namespace")
			return nil, nil
		},
	}

	got, err := ResolveNamespace("minecraft", resolver)
	if err != nil {
		t.Fatalf("ResolveNamespace returned error: %v", err)
	}
	if got != "minecraft" {
		t.Fatalf("namespace = %q, want %q", got, "minecraft")
	}
}

func TestResolveNamespaceEmptyReadsNamespaceFile(t *testing.T) {
	path := writeNamespaceFile(t, "minecraft")

	got, err := ResolveNamespace("", NamespaceResolver{Path: path})
	if err != nil {
		t.Fatalf("ResolveNamespace returned error: %v", err)
	}
	if got != "minecraft" {
		t.Fatalf("namespace = %q, want %q", got, "minecraft")
	}
}

func TestResolveNamespaceTrimsNamespaceFileContent(t *testing.T) {
	path := writeNamespaceFile(t, " \tminecraft\n")

	got, err := ResolveNamespace("", NamespaceResolver{Path: path})
	if err != nil {
		t.Fatalf("ResolveNamespace returned error: %v", err)
	}
	if got != "minecraft" {
		t.Fatalf("namespace = %q, want %q", got, "minecraft")
	}
}

func TestResolveNamespaceMissingFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	_, err := ResolveNamespace("", NamespaceResolver{Path: path})
	if err == nil {
		t.Fatal("expected missing namespace file error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestResolveNamespaceEmptyFileReturnsError(t *testing.T) {
	path := writeNamespaceFile(t, "\n\t ")

	_, err := ResolveNamespace("", NamespaceResolver{Path: path})
	if err == nil {
		t.Fatal("expected empty namespace file error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want empty file error", err)
	}
}

func TestResolveNamespaceInvalidFileNamespaceReturnsError(t *testing.T) {
	path := writeNamespaceFile(t, "bad/namespace")

	_, err := ResolveNamespace("", NamespaceResolver{Path: path})
	if err == nil {
		t.Fatal("expected invalid namespace error")
	}
	if !strings.Contains(err.Error(), "DNS label") {
		t.Fatalf("error = %v, want DNS label error", err)
	}
}

func TestResolveNamespaceInvalidExplicitNamespaceReturnsError(t *testing.T) {
	_, err := ResolveNamespace("bad/namespace", NamespaceResolver{})
	if err == nil {
		t.Fatal("expected invalid explicit namespace error")
	}
	if !strings.Contains(err.Error(), "configured namespace") {
		t.Fatalf("error = %v, want configured namespace context", err)
	}
}

func TestResolveNamespacePermissionDeniedReturnsError(t *testing.T) {
	resolver := NamespaceResolver{
		Path: "namespace",
		ReadFile: func(string) ([]byte, error) {
			return nil, fs.ErrPermission
		},
	}

	_, err := ResolveNamespace("", resolver)
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want fs.ErrPermission", err)
	}
}

func TestResolveNamespaceUsesCustomPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-namespace")
	called := false
	resolver := NamespaceResolver{
		Path: path,
		ReadFile: func(gotPath string) ([]byte, error) {
			called = true
			if gotPath != path {
				t.Fatalf("path = %q, want %q", gotPath, path)
			}
			return []byte("minecraft"), nil
		},
	}

	got, err := ResolveNamespace("", resolver)
	if err != nil {
		t.Fatalf("ResolveNamespace returned error: %v", err)
	}
	if !called {
		t.Fatal("ReadFile was not called")
	}
	if got != "minecraft" {
		t.Fatalf("namespace = %q, want %q", got, "minecraft")
	}
}

func TestResolveNamespaceDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ResolveNamespace panicked: %v", recovered)
		}
	}()

	_, _ = ResolveNamespace("", NamespaceResolver{
		ReadFile: func(string) ([]byte, error) {
			return []byte("bad/namespace"), nil
		},
	})
}

func writeNamespaceFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write namespace file: %v", err)
	}
	return path
}
