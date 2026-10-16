package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
	// Separate full revision metadata from the existing display commit.
	buildCommit = "unknown"
)

// debugFlag is bound to the persistent --debug flag and, when set, makes
// FormatError emit the full original error chain instead of just the
// user-facing message.
var debugFlag bool

var rootCmd = &cobra.Command{
	Use:               "multica",
	Short:             "Multica CLI — local agent runtime and management tool",
	Long:              "Work seamlessly with Multica from the command line.",
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: prepareWorkspaceSelection,
}

func init() {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)\ngo: %s, os/arch: %s/%s", version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	rootCmd.SetVersionTemplate("multica {{.Version}}\n")

	// Tag every CLI HTTP request with this binary's build version so the
	// server can split logs/metrics by client version.
	cli.ClientVersion = version

	rootCmd.PersistentFlags().String("server-url", "", "Multica server URL (env: MULTICA_SERVER_URL)")
	rootCmd.PersistentFlags().String("workspace-id", "", "Workspace ID (env: MULTICA_WORKSPACE_ID)")
	rootCmd.PersistentFlags().String("workspace", "", "Workspace UUID, slug or exact name (case-insensitive); overrides MULTICA_WORKSPACE_ID and --workspace-id for this invocation")
	rootCmd.PersistentFlags().String("profile", "", "Configuration profile name (e.g. dev) — isolates config, daemon state, and workspaces")
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "Print full error details on failure (env: MULTICA_DEBUG)")

	// Core commands
	overviewCmd.GroupID = groupCore
	capabilitiesCmd.GroupID = groupCore
	issueCmd.GroupID = groupCore
	projectCmd.GroupID = groupCore
	labelCmd.GroupID = groupCore
	domainCmd.GroupID = groupCore
	propertyCmd.GroupID = groupCore
	agentCmd.GroupID = groupCore
	autopilotCmd.GroupID = groupCore
	workspaceCmd.GroupID = groupCore
	repoCmd.GroupID = groupCore
	connectionCmd.GroupID = groupCore
	skillCmd.GroupID = groupCore
	squadCmd.GroupID = groupCore
	chatCmd.GroupID = groupCore
	inboxCmd.GroupID = groupCore
	transferCmd.GroupID = groupAdditional
	logsCmd.GroupID = groupCore
	askCmd.GroupID = groupCore

	// Runtime commands
	daemonCmd.GroupID = groupRuntime
	runtimeCmd.GroupID = groupRuntime

	// Additional commands
	authCmd.GroupID = groupAdditional
	botCmd.GroupID = groupAdditional
	userCmd.GroupID = groupAdditional
	loginCmd.GroupID = groupAdditional
	setupCmd.GroupID = groupAdditional
	attachmentCmd.GroupID = groupAdditional
	configCmd.GroupID = groupAdditional
	settingsCmd.GroupID = groupCore
	updateCmd.GroupID = groupAdditional
	versionCmd.GroupID = groupAdditional

	rootCmd.AddCommand(overviewCmd)
	mcpCommand := newMCPCommand()
	mcpCommand.GroupID = groupCore
	rootCmd.AddCommand(mcpCommand)
	for _, runs := range []bool{false, true} {
		cmd := newFastAccessCommand(runs)
		cmd.GroupID = groupCore
		rootCmd.AddCommand(cmd)
	}
	rootCmd.AddCommand(capabilitiesCmd)
	rootCmd.AddCommand(issueCmd)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(labelCmd)
	rootCmd.AddCommand(domainCmd)
	rootCmd.AddCommand(propertyCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(autopilotCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(repoCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(squadCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(inboxCmd)
	rootCmd.AddCommand(transferCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(runtimeCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(botCmd)
	rootCmd.AddCommand(userCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(attachmentCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(settingsCmd)
	githubAppCmd.GroupID = groupCore
	rootCmd.AddCommand(githubAppCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)

	initHelp(rootCmd)
	installUnknownSubcommandGuard(rootCmd)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == execenv.PreparationHelperArg {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		if err := execenv.RunPreparationHelper(os.Stdin, os.Stdout, logger); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cli.CleanupStaleUpdateArtifacts()
	if err := rootCmd.Execute(); err != nil {
		if err != errSilent {
			fmt.Fprintln(os.Stderr, cli.FormatError(authFailureHint(rootCmd, err), debugFlag))
		}
		os.Exit(cli.ExitCodeFor(err))
	}
}
