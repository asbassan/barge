package build

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestZipDir(t *testing.T) {
	// Build a temp directory with a small file tree.
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644))
	sub := filepath.Join(dir, "sub")
	must(t, os.MkdirAll(sub, 0755))
	must(t, os.WriteFile(filepath.Join(sub, "world.txt"), []byte("world"), 0644))

	data, err := zipDir(dir)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	found := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %q: %v", f.Name, err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		found[f.Name] = string(content)
	}

	if got, ok := found["hello.txt"]; !ok || got != "hello" {
		t.Errorf("hello.txt: got %q", got)
	}
	// Sub-directory entries use forward slashes.
	if got, ok := found["sub/world.txt"]; !ok || got != "world" {
		t.Errorf("sub/world.txt: got %q", got)
	}
	if len(found) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(found), found)
	}
}

func TestNewFileServer(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("data"), 0644))

	srv, port, err := newFileServer(dir)
	if err != nil {
		t.Fatalf("newFileServer: %v", err)
	}
	defer srv.Close()

	if port <= 0 || port > 65535 {
		t.Errorf("unexpected port %d", port)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
