// Command pcenter provides a CLI for Microsoft Partner Center Store submissions.
package main

import (
	"context"
	"os"
	"time"

	"github.com/prof18/pcenter-cli/internal/cli"
	"github.com/prof18/pcenter-cli/internal/config"
	"github.com/prof18/pcenter-cli/internal/output"
	"golang.org/x/term"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	exitCode := cli.Execute(context.Background(), os.Args[1:], cli.Dependencies{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Environment: config.CurrentEnvironment(),
		IsTTY:       output.IsTerminal(os.Stdout),
		StdinIsTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		Now:         time.Now,
		Build:       cli.BuildInfo{Version: version, Commit: commit, Date: buildDate},
		Keychain:    config.SystemKeychain{},
	})
	os.Exit(exitCode)
}
