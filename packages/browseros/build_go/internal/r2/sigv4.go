package r2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 (the subset S3/R2 needs). Kept as standalone
// functions so the canonical request / string-to-sign / signature stages can
// be verified against AWS's published test vectors.

const unsignedPayload = "UNSIGNED-PAYLOAD"

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// uriEncode implements the AWS flavor of RFC 3986 encoding. Path segments
// keep "/" when encodeSlash is false.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for _, ch := range []byte(s) {
		switch {
		case (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'),
			ch == '-' || ch == '_' || ch == '.' || ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{ch})))
		}
	}
	return b.String()
}

func canonicalQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		values := append([]string(nil), query[k]...)
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalRequest builds the SigV4 canonical request. headers must include
// host; keys are canonicalized to lowercase.
func canonicalRequest(method, path string, query url.Values, headers map[string]string, payloadHash string) (string, string) {
	lower := map[string]string{}
	var names []string
	for k, v := range headers {
		name := strings.ToLower(strings.TrimSpace(k))
		lower[name] = strings.TrimSpace(v)
		names = append(names, name)
	}
	sort.Strings(names)

	var canonHeaders strings.Builder
	for _, name := range names {
		canonHeaders.WriteString(name + ":" + lower[name] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	req := strings.Join([]string{
		method,
		uriEncode(path, false),
		canonicalQuery(query),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	return req, signedHeaders
}

func credentialScope(ts time.Time, region, service string) string {
	return ts.UTC().Format("20060102") + "/" + region + "/" + service + "/aws4_request"
}

func stringToSign(ts time.Time, region, service, canonicalReq string) string {
	return strings.Join([]string{
		"AWS4-HMAC-SHA256",
		ts.UTC().Format("20060102T150405Z"),
		credentialScope(ts, region, service),
		sha256Hex([]byte(canonicalReq)),
	}, "\n")
}

func signingKey(secret string, ts time.Time, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(ts.UTC().Format("20060102")))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func signature(secret string, ts time.Time, region, service, strToSign string) string {
	return hex.EncodeToString(hmacSHA256(signingKey(secret, ts, region, service), []byte(strToSign)))
}

func authorizationHeader(accessKey, secret string, ts time.Time, region, service, signedHeaders, sig string) string {
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + credentialScope(ts, region, service) +
		", SignedHeaders=" + signedHeaders + ", Signature=" + sig
}
