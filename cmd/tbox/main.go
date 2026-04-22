// cmd/tbox/main.go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/tbox-run/tbox/internal/engine"
)

func main() {
	if err := preflight(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]
	rest := os.Args[2:]

	var err error
	switch subcommand {
	case "run":
		err = cmdRun(rest)
	case "ps":
		err = cmdPS(rest)
	case "stop":
		err = cmdStop(rest)
	case "logs":
		err = cmdLogs(rest)
	case "exec":
		err = cmdExec(rest)
	case "rm":
		err = cmdRm(rest)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown subcommand %q\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stdout, `tbox - Docker-like container runtime for Android/Termux (no root required)

Usage:
  tbox run   [flags] <image.tgz> [--] <command> [args...]
  tbox ps
  tbox stop  <cid|name>
  tbox logs  [-f] <cid|name>
  tbox exec  [flags] <cid|name> [--] <command> [args...]
  tbox rm    [--force] <cid|name>

Flags for run:
  -d              Run container in the background and print container ID
  --name string   Assign a human-readable name to the container
  -e KEY=VAL      Set an environment variable (repeatable)
  -b host:ctr     Bind-mount a host path (host:container[:ro]) (repeatable)
  -w path         Working directory inside the container (default /)
  -v              Print raw proot stderr to the console

Flags for exec:
  -e KEY=VAL      Inject an environment variable (repeatable)
  -w path         Working directory inside the container (default /)

Flags for rm:
  --force         Stop a running container before removing it

Examples:
  tbox run -d --name db --bind ~/db:/data ./sqlite.tgz -- /bin/dbserver
  tbox run --name app --bind ~/app:/data ./todo.tgz -- /bin/todo
  tbox ps
  tbox exec db -- /bin/sh
  tbox logs -f app
  tbox stop app
  tbox rm db`)
}

func preflight() error {
	if _, err := exec.LookPath("proot"); err != nil {
		return fmt.Errorf("proot not found in PATH\nInstall in Termux: pkg install proot")
	}
	return nil
}

// setupSignalForwarding forwards SIGINT/SIGTERM to the active foreground proot
// process. Must only be called for foreground (non-detached) runs.
func setupSignalForwarding() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		pid := engine.GetCurrentPID()
		if pid > 0 {
			_ = syscall.Kill(int(pid), sig.(syscall.Signal))
		}
		if sig == syscall.SIGINT {
			os.Exit(130)
		}
		os.Exit(1)
	}()
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: tbox run [flags] <image.tgz> [--] <command> [args...]")
	}

	var (
		detach  bool
		verbose bool
		name    string
		workdir string
		envs    multiFlag
		binds   multiFlag
	)

	fs.BoolVar(&detach, "d", false, "Run in background")
	fs.BoolVar(&verbose, "v", false, "Print proot stderr")
	fs.StringVar(&name, "name", "", "Human-readable container name")
	fs.StringVar(&workdir, "w", "/", "Working directory inside container")
	fs.Var(&envs, "e", "Environment variable KEY=VAL (repeatable)")
	fs.Var(&binds, "b", "Bind mount host:container[:ro] (repeatable)")
	fs.Var(&binds, "bind", "Bind mount host:container[:ro] (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) > 0 && remaining[0] == "--" {
		remaining = remaining[1:]
	}
	if len(remaining) < 2 {
		fs.Usage()
		return fmt.Errorf("requires <image.tgz> and at least one command argument")
	}

	cfg := engine.Config{
		ImagePath:  remaining[0],
		Entrypoint: remaining[1:],
		Env:        []string(envs),
		Workdir:    workdir,
		Binds:      []string(binds),
		Verbose:    verbose,
		Detach:     detach,
		Name:       name,
	}

	if !detach {
		setupSignalForwarding()
	}

	exitCode, err := engine.RunContainer(cfg)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

func cmdPS(args []string) error {
	return engine.ListContainers()
}

func cmdStop(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: tbox stop <cid|name>")
	}
	return engine.StopContainer(args[0])
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "Follow log output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tbox logs [-f] <cid|name>")
	}
	return engine.TailLogs(fs.Arg(0), *follow)
}

func cmdExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: tbox exec [flags] <cid|name> [--] <command> [args...]")
	}

	var (
		workdir string
		envs    multiFlag
	)

	fs.StringVar(&workdir, "w", "", "Working directory inside container")
	fs.Var(&envs, "e", "Environment variable KEY=VAL (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) > 0 && remaining[0] == "--" {
		remaining = remaining[1:]
	}
	if len(remaining) < 2 {
		fs.Usage()
		return fmt.Errorf("requires <cid|name> and at least one command argument")
	}

	cfg := engine.ExecConfig{
		CIDOrName: remaining[0],
		Command:   remaining[1:],
		Env:       []string(envs),
		Workdir:   workdir,
	}

	exitCode, err := engine.ExecContainer(cfg)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

func cmdRm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "Stop running container before removing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tbox rm [--force] <cid|name>")
	}
	return engine.RmContainer(fs.Arg(0), *force)
}

// multiFlag accumulates repeated flag values into a slice.
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	return fmt.Sprint([]string(*m))
}

func (m *multiFlag) Set(val string) error {
	*m = append(*m, val)
	return nil
}
