// Package r2 is a minimal Cloudflare R2 (S3-compatible) client over SigV4,
// replacing boto3 (build/modules/storage/r2.py). Path-style addressing,
// single-PUT uploads (artifacts are far below the 5 GB limit).
package r2

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// ErrNotFound is returned for missing keys (404 / NoSuchKey).
var ErrNotFound = errors.New("r2: key not found")

// Client talks to one R2 bucket.
type Client struct {
	Endpoint  string // https://<account>.r2.cloudflarestorage.com
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // R2 uses "auto"
	HTTP      *http.Client
	Now       func() time.Time
}

// NewFromEnv builds a client from R2_* env vars (envx defaults included).
func NewFromEnv() (*Client, error) {
	if !envx.HasR2Config() {
		return nil, fmt.Errorf(
			"R2 configuration not set. Required env vars: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}
	return &Client{
		Endpoint:  envx.R2EndpointURL(),
		Bucket:    envx.R2Bucket(),
		AccessKey: envx.R2AccessKeyID(),
		SecretKey: envx.R2SecretAccessKey(),
		Region:    "auto",
		HTTP:      &http.Client{Timeout: 30 * time.Minute},
		Now:       time.Now,
	}, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// do signs and performs one S3 request. Path-style: /<bucket>/<key>.
func (c *Client) do(method, key string, query url.Values, extraHeaders map[string]string, body io.Reader, bodyLen int64, payloadHash string) (*http.Response, error) {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid R2 endpoint %q: %w", c.Endpoint, err)
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + key
	}
	if query == nil {
		query = url.Values{}
	}

	ts := c.now()
	headers := map[string]string{
		"host":                 endpoint.Host,
		"x-amz-date":           ts.UTC().Format("20060102T150405Z"),
		"x-amz-content-sha256": payloadHash,
	}
	for k, v := range extraHeaders {
		headers[strings.ToLower(k)] = v
	}

	canonicalReq, signedHeaders := canonicalRequest(method, path, query, headers, payloadHash)
	strToSign := stringToSign(ts, c.Region, "s3", canonicalReq)
	sig := signature(c.SecretKey, ts, c.Region, "s3", strToSign)

	reqURL := *endpoint
	reqURL.Path = path
	reqURL.RawQuery = canonicalQuery(query)
	req, err := http.NewRequest(method, reqURL.String(), body)
	if err != nil {
		return nil, err
	}
	if bodyLen >= 0 {
		req.ContentLength = bodyLen
	}
	for k, v := range headers {
		if k == "host" {
			continue
		}
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization",
		authorizationHeader(c.AccessKey, c.SecretKey, ts, c.Region, "s3", signedHeaders, sig))

	return c.httpClient().Do(req)
}

func closeAndError(resp *http.Response, op, key string) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode == http.StatusNotFound || strings.Contains(string(body), "NoSuchKey") {
		return fmt.Errorf("%s %s: %w", op, key, ErrNotFound)
	}
	return fmt.Errorf("%s %s: HTTP %d: %s", op, key, resp.StatusCode, strings.TrimSpace(string(body)))
}

// PutFile uploads a local file (r2.py upload_file_to_r2). Uses
// UNSIGNED-PAYLOAD over HTTPS so multi-hundred-MB artifacts aren't read twice.
func (c *Client) PutFile(localPath, key, contentType string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	logx.Info(fmt.Sprintf("Uploading %s...", filepath.Base(localPath)))
	headers := map[string]string{}
	if contentType != "" {
		headers["content-type"] = contentType
	}
	resp, err := c.do(http.MethodPut, key, nil, headers, f, info.Size(), unsignedPayload)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return closeAndError(resp, "put", key)
	}
	resp.Body.Close()
	logx.Success("Uploaded: " + key)
	return nil
}

// PutBytes uploads an in-memory object with a signed payload hash.
func (c *Client) PutBytes(data []byte, key, contentType string) error {
	headers := map[string]string{}
	if contentType != "" {
		headers["content-type"] = contentType
	}
	resp, err := c.do(http.MethodPut, key, nil, headers, strings.NewReader(string(data)), int64(len(data)), sha256Hex(data))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return closeAndError(resp, "put", key)
	}
	resp.Body.Close()
	return nil
}

// GetFile downloads a key to a local path (r2.py download_file_from_r2).
func (c *Client) GetFile(key, destPath string) error {
	logx.Info(fmt.Sprintf("Downloading %s...", key))
	resp, err := c.do(http.MethodGet, key, nil, nil, nil, -1, sha256Hex(nil))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return closeAndError(resp, "get", key)
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(destPath)
		return fmt.Errorf("get %s: %w", key, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	logx.Success("Downloaded: " + filepath.Base(destPath))
	return nil
}

// GetObject fetches a key into memory; ErrNotFound for missing keys.
func (c *Client) GetObject(key string) ([]byte, error) {
	resp, err := c.do(http.MethodGet, key, nil, nil, nil, -1, sha256Hex(nil))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, closeAndError(resp, "get", key)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Exists checks a key via HEAD.
func (c *Client) Exists(key string) (bool, error) {
	resp, err := c.do(http.MethodHead, key, nil, nil, nil, -1, sha256Hex(nil))
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("head %s: HTTP %d", key, resp.StatusCode)
}

// Delete removes a key (used for upload rollback).
func (c *Client) Delete(key string) error {
	resp, err := c.do(http.MethodDelete, key, nil, nil, nil, -1, sha256Hex(nil))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}

// Copy performs a server-side object copy within the bucket.
func (c *Client) Copy(srcKey, dstKey string) error {
	headers := map[string]string{
		"x-amz-copy-source": "/" + c.Bucket + "/" + uriEncode(srcKey, false),
	}
	resp, err := c.do(http.MethodPut, dstKey, nil, headers, nil, -1, sha256Hex(nil))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return closeAndError(resp, "copy", srcKey+" -> "+dstKey)
	}
	resp.Body.Close()
	return nil
}

// Object is one listed key.
type Object struct {
	Key          string `xml:"Key"`
	Size         int64  `xml:"Size"`
	ETag         string `xml:"ETag"`
	LastModified string `xml:"LastModified"`
}

type listResult struct {
	Contents              []Object `xml:"Contents"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	CommonPrefixes        []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// List returns all objects under prefix (ListObjectsV2, following
// continuation tokens).
func (c *Client) List(prefix string) ([]Object, error) {
	var all []Object
	token := ""
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		resp, err := c.do(http.MethodGet, "", query, nil, nil, -1, sha256Hex(nil))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, closeAndError(resp, "list", prefix)
		}
		var page listResult
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		all = append(all, page.Contents...)
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return all, nil
		}
		token = page.NextContinuationToken
	}
}

// ListPrefixes returns the common prefixes directly under prefix using a
// "/" delimiter (used to enumerate release versions).
func (c *Client) ListPrefixes(prefix string) ([]string, error) {
	query := url.Values{"list-type": {"2"}, "prefix": {prefix}, "delimiter": {"/"}}
	resp, err := c.do(http.MethodGet, "", query, nil, nil, -1, sha256Hex(nil))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, closeAndError(resp, "list", prefix)
	}
	defer resp.Body.Close()
	var page listResult
	if err := xml.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("list %s: %w", prefix, err)
	}
	prefixes := make([]string, 0, len(page.CommonPrefixes))
	for _, p := range page.CommonPrefixes {
		prefixes = append(prefixes, p.Prefix)
	}
	return prefixes, nil
}
