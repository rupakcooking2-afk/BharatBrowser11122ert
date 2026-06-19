package sign

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/sparkle"
)

// SparkleSign signs dist DMGs with the Sparkle Ed25519 key
// (sign/sparkle.py SparkleSignModule).
type SparkleSign struct{}

func NewSparkleSign() *SparkleSign { return &SparkleSign{} }

func (SparkleSign) Name() string { return "sparkle_sign" }
func (SparkleSign) Description() string {
	return "Sign DMG files with Sparkle Ed25519 key for auto-update"
}

func (SparkleSign) Validate(ctx *buildctx.Context) error {
	if !envx.HasSparkleKey() {
		return fmt.Errorf("SPARKLE_PRIVATE_KEY environment variable not set")
	}
	return nil
}

func (SparkleSign) Execute(ctx *buildctx.Context) error {
	logx.Info("\n🔐 Signing DMGs with Sparkle...")

	distDir := ctx.DistDir()
	if _, err := os.Stat(distDir); err != nil {
		logx.Warning("Dist directory not found: " + distDir)
		return nil
	}
	dmgFiles, err := filepath.Glob(filepath.Join(distDir, "*.dmg"))
	if err != nil || len(dmgFiles) == 0 {
		logx.Warning("No DMG files found to sign")
		return nil
	}

	signed := 0
	for _, dmgPath := range dmgFiles {
		name := filepath.Base(dmgPath)
		logx.Info(fmt.Sprintf("🔐 Signing %s...", name))
		sig, length, err := sparkle.SignFileWithEnv(dmgPath)
		if err != nil {
			logx.Error(fmt.Sprintf("Error signing %s: %v", name, err))
			continue
		}
		ctx.SparkleSignatures[name] = buildctx.SparkleSig{Signature: sig, Length: length}
		logx.Info(fmt.Sprintf("  %s: sig=%.20s... length=%d", name, sig, length))
		signed++
	}
	logx.Success(fmt.Sprintf("✅ Signed %d DMG(s) with Sparkle", signed))
	return nil
}
