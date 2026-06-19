package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/ui"
	"github.com/spf13/cobra"
)

var Version = "dev"

var groupOrder = []string{
	"Build:",
	"Development:",
	"Release & Distribution:",
}

func helpHeader(s string) string { return ui.Header(s) }
func helpCmdCol(s string) string { return ui.Command(s) }
func helpHint(s string) string   { return ui.Hint(s) }
func helpAliases(aliases []string) string {
	return ui.Aliases(aliases)
}

func groupedHelp(cmd *cobra.Command) string {
	groups := map[string][]*cobra.Command{}
	for _, child := range cmd.Commands() {
		if !child.IsAvailableCommand() && child.Name() != "help" {
			continue
		}
		group := child.Annotations["group"]
		if group == "" {
			group = "Build:"
		}
		groups[group] = append(groups[group], child)
	}
	var builder strings.Builder
	for _, group := range groupOrder {
		commands, ok := groups[group]
		if !ok {
			continue
		}
		builder.WriteString("\n" + helpHeader(group) + "\n")
		for _, child := range commands {
			line := "  " + helpCmdCol(fmt.Sprintf("%-14s", child.Name())) + " " + child.Short
			if len(child.Aliases) > 0 {
				line += " " + helpAliases(child.Aliases)
			}
			builder.WriteString(line + "\n")
		}
	}
	return builder.String()
}

const usageTemplate = `{{helpHeader "Usage:"}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

{{helpHeader "Aliases:"}}
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

{{helpHeader "Examples:"}}
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}
{{groupedHelp .}}{{end}}{{if .HasAvailableLocalFlags}}

{{helpHeader "Flags:"}}
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

{{helpHeader "Global Flags:"}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

{{helpHint (printf "Use \"%s [command] --help\" for more information." .CommandPath)}}{{end}}
`

var rootCmd = &cobra.Command{
	Use:   "browseros",
	Short: "BrowserOS Build System",
	Long: `BrowserOS Build System — build, develop, and release the BrowserOS browser.

Standalone Go port of the Python build tool (packages/browseros/build/).
Reads the same configs (build/config/*.yaml) and operates on the same repo
checkout; run it from anywhere inside the BrowserOS repo or set BROWSEROS_ROOT.`,
	Example: `  browseros build --config build/config/release.macos.arm64.yaml --chromium-src ~/chromium/src
  browseros build --chromium-src ~/chromium/src --setup --prep --build
  browseros dev --chromium-src ~/chromium/src extract commit HEAD
  browseros release --list`,
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logx.EnableFileLog()
		envx.LoadDotenv()
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	cobra.AddTemplateFunc("helpHeader", helpHeader)
	cobra.AddTemplateFunc("helpCmdCol", helpCmdCol)
	cobra.AddTemplateFunc("helpAliases", helpAliases)
	cobra.AddTemplateFunc("helpHint", helpHint)
	cobra.AddTemplateFunc("groupedHelp", groupedHelp)
	rootCmd.SetUsageTemplate(usageTemplate)
	rootCmd.CompletionOptions.DisableDefaultCmd = true
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.Error("Error: ")+err.Error())
		os.Exit(1)
	}
}
