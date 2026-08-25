package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ssfun/surge-external-bridge/internal/gateway"
	"github.com/ssfun/surge-external-bridge/internal/management"
	core "github.com/ssfun/surge-external-bridge/internal/mihomo"
	serviceManager "github.com/ssfun/surge-external-bridge/internal/service"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "SurgeEB:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(nil)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("SurgeEB %s (Embedded Mihomo %s)\n", gateway.Version, core.CoreVersion)
		return nil
	case "service":
		return serviceCommand(args[1:])
	case "help", "--help", "-h":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataDir := flags.String("data-dir", defaultDataDir(), "configuration and state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// Provider caches contain upstream node credentials. Install a process-wide
	// private umask before any product or Mihomo state can be created, including
	// interactive runs outside the service definitions that already use 0077.
	syscall.Umask(0o077)
	application, err := gateway.New(*dataDir)
	if err != nil {
		return err
	}
	defer application.Close()
	server, err := management.New(application)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	config := application.Config()
	fmt.Printf("Surge External Bridge %s · Embedded Mihomo %s\n", gateway.Version, core.CoreVersion)
	fmt.Printf("configuration console: http://%s\n", config.HTTPBind)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("service requires install, uninstall, or status")
	}
	switch args[0] {
	case "status":
		info, err := serviceManager.Status()
		if err != nil {
			return err
		}
		fmt.Printf("platform=%s scope=%s installed=%t active=%t path=%s\n", info.Platform, info.Scope, info.Installed, info.Active, info.Path)
		return nil
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		dataDir := flags.String("data-dir", defaultDataDir(), "configuration and state directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		info, err := serviceManager.Install(*dataDir)
		if err != nil {
			return err
		}
		fmt.Printf("installed %s: %s\n", info.Scope, info.Path)
		return nil
	case "uninstall":
		info, err := serviceManager.Uninstall()
		if err != nil {
			return err
		}
		fmt.Printf("removed service definition: %s\n", info.Path)
		return nil
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func defaultDataDir() string {
	if override := os.Getenv("SURGEEB_DATA_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".surge-external-bridge"
	}
	return filepath.Join(home, ".surge-external-bridge")
}

func printHelp() {
	fmt.Println(`SurgeEB - Surge External Bridge powered by embedded Mihomo

Usage:
  SurgeEB serve [--data-dir PATH]
  SurgeEB version
  SurgeEB service status
  SurgeEB service install [--data-dir PATH]
  SurgeEB service uninstall`)
}
