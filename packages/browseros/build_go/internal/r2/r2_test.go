package r2

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSigV4AgainstAWSGetVanillaVector pins the signer to the official AWS
// SigV4 test suite "get-vanilla" case.
func TestSigV4AgainstAWSGetVanillaVector(t *testing.T) {
	ts := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	headers := map[string]string{
		"host":       "example.amazonaws.com",
		"x-amz-date": "20150830T123600Z",
	}
	emptyHash := sha256Hex(nil)

	canonicalReq, signedHeaders := canonicalRequest("GET", "/", url.Values{}, headers, emptyHash)
	wantCanonical := "GET\n/\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\n" + emptyHash
	if canonicalReq != wantCanonical {
		t.Errorf("canonical request:\n%q\nwant:\n%q", canonicalReq, wantCanonical)
	}
	if signedHeaders != "host;x-amz-date" {
		t.Errorf("signed headers = %q", signedHeaders)
	}

	strToSign := stringToSign(ts, "us-east-1", "service", canonicalReq)
	wantSTS := "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/service/aws4_request\n" +
		"bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63"
	if strToSign != wantSTS {
		t.Errorf("string to sign:\n%q\nwant:\n%q", strToSign, wantSTS)
	}

	sig := signature("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", ts, "us-east-1", "service", strToSign)
	want := "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if sig != want {
		t.Errorf("signature = %s, want %s", sig, want)
	}
}

func TestURIEncodeMatchesAWSRules(t *testing.T) {
	if got := uriEncode("releases/0.46.17/macos/My App.dmg", false); got != "releases/0.46.17/macos/My%20App.dmg" {
		t.Errorf("path encode = %q", got)
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("query encode = %q", got)
	}
	if got := uriEncode("tilde~dash-dot.под", true); !strings.HasPrefix(got, "tilde~dash-dot.") {
		t.Errorf("unreserved chars must pass through: %q", got)
	}
}

// fixtureClient points a Client at an httptest server.
func fixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{
		Endpoint:  server.URL,
		Bucket:    "browseros",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		Region:    "auto",
		HTTP:      server.Client(),
		Now:       func() time.Time { return time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC) },
	}
}

func TestPutFileSendsSignedPathStyleRequest(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotSHA, gotCT string
	var gotBody []byte
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSHA = r.Header.Get("x-amz-content-sha256")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	local := filepath.Join(t.TempDir(), "artifact.dmg")
	if err := os.WriteFile(local, []byte("dmg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.PutFile(local, "releases/0.46.17/macos/BrowserOS.dmg", "application/octet-stream"); err != nil {
		t.Fatal(err)
	}

	if gotMethod != "PUT" || gotPath != "/browseros/releases/0.46.17/macos/BrowserOS.dmg" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=test-access/20260610/auto/s3/aws4_request") {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=") || !strings.Contains(gotAuth, "Signature=") {
		t.Errorf("auth missing parts: %q", gotAuth)
	}
	if gotSHA != "UNSIGNED-PAYLOAD" {
		t.Errorf("content sha = %q, want UNSIGNED-PAYLOAD for file uploads", gotSHA)
	}
	if gotCT != "application/octet-stream" {
		t.Errorf("content type = %q", gotCT)
	}
	if string(gotBody) != "dmg-bytes" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestGetFileWritesDestinationAndErrNotFound(t *testing.T) {
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "missing.bin") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("binary-content"))
	})

	dest := filepath.Join(t.TempDir(), "nested", "out.bin")
	if err := client.GetFile("resources/out.bin", dest); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(dest)
	if err != nil || string(content) != "binary-content" {
		t.Errorf("dest content = (%q, %v)", content, err)
	}

	err = client.GetFile("resources/missing.bin", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "key not found") {
		t.Errorf("missing key err = %v", err)
	}
}

func TestCopySendsCopySourceHeader(t *testing.T) {
	var gotSource, gotPath string
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotSource = r.Header.Get("x-amz-copy-source")
		gotPath = r.URL.Path
		w.Write([]byte("<CopyObjectResult></CopyObjectResult>"))
	})
	if err := client.Copy("releases/0.46.17/macos/a.dmg", "download/macos/a.dmg"); err != nil {
		t.Fatal(err)
	}
	if gotSource != "/browseros/releases/0.46.17/macos/a.dmg" {
		t.Errorf("copy source = %q", gotSource)
	}
	if gotPath != "/browseros/download/macos/a.dmg" {
		t.Errorf("copy dest path = %q", gotPath)
	}
}

func TestListFollowsContinuationTokens(t *testing.T) {
	page := 0
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			t.Errorf("list-type = %q", r.URL.Query().Get("list-type"))
		}
		page++
		if page == 1 {
			if r.URL.Query().Get("continuation-token") != "" {
				t.Error("first page must not send a token")
			}
			w.Write([]byte(`<ListBucketResult>
				<IsTruncated>true</IsTruncated>
				<NextContinuationToken>tok123</NextContinuationToken>
				<Contents><Key>releases/a.dmg</Key><Size>10</Size></Contents>
			</ListBucketResult>`))
			return
		}
		if got := r.URL.Query().Get("continuation-token"); got != "tok123" {
			t.Errorf("token = %q", got)
		}
		w.Write([]byte(`<ListBucketResult>
			<IsTruncated>false</IsTruncated>
			<Contents><Key>releases/b.dmg</Key><Size>20</Size></Contents>
		</ListBucketResult>`))
	})

	objects, err := client.List("releases/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Key != "releases/a.dmg" || objects[1].Key != "releases/b.dmg" {
		t.Errorf("objects = %+v", objects)
	}
}

func TestNewFromEnvRequiresCredentials(t *testing.T) {
	for _, name := range []string{"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	if _, err := NewFromEnv(); err == nil || !strings.Contains(err.Error(), "R2_ACCOUNT_ID") {
		t.Errorf("err = %v", err)
	}

	t.Setenv("R2_ACCOUNT_ID", "acct")
	t.Setenv("R2_ACCESS_KEY_ID", "ak")
	t.Setenv("R2_SECRET_ACCESS_KEY", "sk")
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if client.Endpoint != "https://acct.r2.cloudflarestorage.com" || client.Bucket != "browseros" || client.Region != "auto" {
		t.Errorf("client = %+v", client)
	}
}
