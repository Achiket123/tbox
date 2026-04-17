// cmd/tbox/main.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tbox-run/tbox/internal/engine"
)

func main() {
	// Preflight: ensure proot is available
	if err := preflight(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Setup signal forwarding BEFORE any subcommand runs
	setupSignalForwarding()

	// Root command
	rootCmd := &cobra.Command{
		Use:   "tbox",
		Short: "Docker-like CLI for Android/Termux (userspace, no root)",
	}

	// run command
	runCmd := &cobra.Command{
		Use:   "run <image.tgz> <command> [args...]",
		Short: "Run a command in a container",
		Args:  cobra.MinimumNArgs(2), // image.tgz + at least one command arg
		RunE:  runContainer,
	}
	runCmd.Flags().StringArrayP("env", "e", []string{}, "Set environment variables")
	runCmd.Flags().StringP("workdir", "w", "/", "Working directory inside container")
	runCmd.Flags().StringArrayP("bind", "b", []string{}, "Bind mount host:path[:ro]")
	runCmd.Flags().BoolP("verbose", "v", false, "Show proot stderr output")
	rootCmd.AddCommand(runCmd)

	// ps command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "ps",
		Short: "List containers",
		RunE:  listContainers,
	})

	// stop command
	stopCmd := &cobra.Command{
		Use:   "stop <cid>",
		Short: "Stop a running container",
		Args:  cobra.ExactArgs(1),
		RunE:  stopContainer,
	}
	rootCmd.AddCommand(stopCmd)

	// logs command
	logsCmd := &cobra.Command{
		Use:   "logs [-f] <cid>",
		Short: "Show container logs",
		Args:  cobra.ExactArgs(1),
		RunE:  showLogs,
	}
	logsCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)

	// Execute
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// preflight checks that proot is installed and accessible
func preflight() error {
	if _, err := exec.LookPath("proot"); err != nil {
		return fmt.Errorf(
			"proot not found in PATH\nInstall in Termux: pkg install proot")
	}
	return nil
}

// setupSignalForwarding forwards SIGINT/SIGTERM to the proot child.
// Reads the PID from engine.GetCurrentPID() — the same atomic the engine
// writes — so the signal actually reaches proot (B1 fix).
func setupSignalForwarding() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		pid := engine.GetCurrentPID()
		if pid > 0 {
			// Forward signal to proot process
			_ = syscall.Kill(int(pid), sig.(syscall.Signal))
		}
		// Exit with standard Ctrl-C code
		if sig == syscall.SIGINT {
			os.Exit(130)
		}
		os.Exit(1)
	}()
}

// runContainer implements 'tbox run'

func runContainer(cmd *cobra.Command, args []string) error {
	// Parse: image.tgz <command> [args...]
	// Note: Cobra strips '--', so we don't require it
	if len(args) < 2 {
		return fmt.Errorf("usage: tbox run <image.tgz> <command> [args...]")
	}

	imagePath := args[0]
	containerCmd := args[1:] // Everything after image path is the command

	// Extract flags
	envVars, _ := cmd.Flags().GetStringArray("env")
	workdir, _ := cmd.Flags().GetString("workdir")
	binds, _ := cmd.Flags().GetStringArray("bind")
	verbose, _ := cmd.Flags().GetBool("verbose")

	cfg := engine.Config{
		ImagePath:  imagePath,
		Entrypoint: containerCmd,
		Env:        envVars,
		Workdir:    workdir,
		Binds:      binds,
		Verbose:    verbose,
	}

	// Run container (blocking)
	exitCode, err := engine.RunContainer(cfg)

	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

// listContainers implements 'tbox ps'
func listContainers(cmd *cobra.Command, args []string) error {
	return engine.ListContainers()
}

// stopContainer implements 'tbox stop <cid>'
func stopContainer(cmd *cobra.Command, args []string) error {
	cid := args[0]
	return engine.StopContainer(cid)
}

// showLogs implements 'tbox logs [-f] <cid>'
func showLogs(cmd *cobra.Command, args []string) error {
	cid := args[0]
	follow, _ := cmd.Flags().GetBool("follow")
	return engine.TailLogs(cid, follow)
}
