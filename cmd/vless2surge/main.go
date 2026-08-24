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

	"github.com/ssfun/vless2surge/internal/app"
	"github.com/ssfun/vless2surge/internal/core"
	"github.com/ssfun/vless2surge/internal/httpapi"
	serviceManager "github.com/ssfun/vless2surge/internal/service"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vless2surge:", err)
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
		fmt.Printf("vless2surge %s (Embedded sing-box %s)\n", app.Version, core.CoreVersion)
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
	if err := core.ValidateBuildFeatures(); err != nil {
		return err
	}
	application, err := app.New(*dataDir)
	if err != nil {
		return err
	}
	defer application.Close()
	server, err := httpapi.New(application)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go application.RunScheduler(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	config := application.Config()
	fmt.Printf("vless2surge %s · Embedded sing-box %s\n", app.Version, core.CoreVersion)
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
	if override := os.Getenv("VLESS2SURGE_DATA_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".vless2surge"
	}
	return filepath.Join(home, ".vless2surge")
}

func printHelp() {
	fmt.Println(`vless2surge - Embedded VLESS gateway for Surge

Usage:
  vless2surge serve [--data-dir PATH]
  vless2surge version
  vless2surge service status
  vless2surge service install [--data-dir PATH]
  vless2surge service uninstall`)
}
