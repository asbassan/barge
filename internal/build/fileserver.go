package build

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// fileServer serves a zipped snapshot of a host directory over HTTP on a
// random local port. The container uses this to download files during COPY.
type fileServer struct {
	listener net.Listener
	server   *http.Server
}

// newFileServer starts an HTTP server that serves a zip of srcDir.
// Returns the server (call Close when done) and the port it bound to.
func newFileServer(srcDir string) (*fileServer, int, error) {
	data, err := zipDir(srcDir)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot archive %q: %w", srcDir, err)
	}

	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, 0, fmt.Errorf("cannot start file server: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/archive.zip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		_, _ = w.Write(data)
	})

	fs := &fileServer{
		listener: l,
		server:   &http.Server{Handler: mux},
	}
	go func() { _ = fs.server.Serve(l) }()
	return fs, port, nil
}

func (fs *fileServer) Close() { _ = fs.server.Close() }

// zipDir builds an in-memory zip archive of all files under dir.
func zipDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Always use forward slashes inside the zip so PowerShell's
		// Expand-Archive handles the entry names cross-platform.
		rel = strings.ReplaceAll(rel, "\\", "/")

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
