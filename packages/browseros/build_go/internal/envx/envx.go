// Package envx centralizes environment-variable access and .env loading,
// mirroring build/common/env.py.
package envx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
)

// LoadDotenv loads the first .env found at <package-root>/.env then
// <repo-root>/.env. Existing process env vars are never overwritten
// (python-dotenv's default). Missing files are not an error.
func LoadDotenv() {
	root, err := paths.Root()
	if err != nil {
		return
	}
	repoRoot := filepath.Dir(filepath.Dir(root)) // packages/browseros -> repo root
	for _, candidate := range []string{
		filepath.Join(root, ".env"),
		filepath.Join(repoRoot, ".env"),
	} {
		if loadDotenvFile(candidate) {
			return
		}
	}
}

func loadDotenvFile(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		key, value, ok := parseDotenvLine(line)
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return true
}

func parseDotenvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	} else if idx := strings.Index(value, " #"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return key, value, true
}

// === Build configuration ===

func ChromiumSrc() string { return os.Getenv("CHROMIUM_SRC") }
func Arch() string        { return os.Getenv("ARCH") }

// DepotToolsWinToolchain defaults to "0" (system toolchain).
func DepotToolsWinToolchain() string {
	if v := os.Getenv("DEPOT_TOOLS_WIN_TOOLCHAIN"); v != "" {
		return v
	}
	return "0"
}

// === macOS code signing ===

func MacOSCertificateName() string      { return os.Getenv("MACOS_CERTIFICATE_NAME") }
func MacOSNotarizationAppleID() string  { return os.Getenv("PROD_MACOS_NOTARIZATION_APPLE_ID") }
func MacOSNotarizationTeamID() string   { return os.Getenv("PROD_MACOS_NOTARIZATION_TEAM_ID") }
func MacOSNotarizationPassword() string { return os.Getenv("PROD_MACOS_NOTARIZATION_PWD") }
func MacOSKeychainPassword() string     { return os.Getenv("MACOS_KEYCHAIN_PASSWORD") }

// === Windows code signing ===

func CodeSignToolPath() string    { return os.Getenv("CODE_SIGN_TOOL_PATH") }
func CodeSignToolExe() string     { return os.Getenv("CODE_SIGN_TOOL_EXE") }
func ESignerUsername() string     { return os.Getenv("ESIGNER_USERNAME") }
func ESignerPassword() string     { return os.Getenv("ESIGNER_PASSWORD") }
func ESignerTOTPSecret() string   { return os.Getenv("ESIGNER_TOTP_SECRET") }
func ESignerCredentialID() string { return os.Getenv("ESIGNER_CREDENTIAL_ID") }

// === Cloudflare R2 ===

func R2AccountID() string       { return os.Getenv("R2_ACCOUNT_ID") }
func R2AccessKeyID() string     { return os.Getenv("R2_ACCESS_KEY_ID") }
func R2SecretAccessKey() string { return os.Getenv("R2_SECRET_ACCESS_KEY") }

// R2Bucket defaults to "browseros".
func R2Bucket() string {
	if v := os.Getenv("R2_BUCKET"); v != "" {
		return v
	}
	return "browseros"
}

// R2CDNBaseURL defaults to the public CDN.
func R2CDNBaseURL() string {
	if v := os.Getenv("R2_CDN_BASE_URL"); v != "" {
		return v
	}
	return "http://cdn.browseros.com"
}

// R2EndpointURL is computed from the account ID; empty when unset.
func R2EndpointURL() string {
	if id := R2AccountID(); id != "" {
		return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", id)
	}
	return ""
}

func HasR2Config() bool {
	return R2AccountID() != "" && R2AccessKeyID() != "" && R2SecretAccessKey() != ""
}

// === Sparkle ===

func SparklePrivateKey() string     { return os.Getenv("SPARKLE_PRIVATE_KEY") }
func SparkleSignUpdatePath() string { return os.Getenv("SPARKLE_SIGN_UPDATE_PATH") }
func HasSparkleKey() bool           { return SparklePrivateKey() != "" }

// === Notifications ===

func SlackWebhookURL() string { return os.Getenv("SLACK_WEBHOOK_URL") }
