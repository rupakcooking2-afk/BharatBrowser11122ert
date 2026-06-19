// Package fetch downloads files over HTTP(S); modules take a Fetcher so
// tests can serve fixtures from httptest.
package fetch

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Fetcher downloads a URL to a destination path.
type Fetcher interface {
	Download(url, dest string) error
}

type httpFetcher struct {
	client *http.Client
}

// Default returns the production Fetcher (no overall timeout: artifacts are
// large; per-connection dial/TLS timeouts come from http.DefaultTransport).
func Default() Fetcher {
	return httpFetcher{client: &http.Client{}}
}

// WithTimeout returns a Fetcher with an overall request timeout.
func WithTimeout(d time.Duration) Fetcher {
	return httpFetcher{client: &http.Client{Timeout: d}}
}

func (f httpFetcher) Download(url, dest string) error {
	resp, err := f.client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// Get fetches a URL fully into memory.
func Get(f Fetcher, url string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "fetch-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := f.Download(url, tmpPath); err != nil {
		return nil, err
	}
	return os.ReadFile(tmpPath)
}
