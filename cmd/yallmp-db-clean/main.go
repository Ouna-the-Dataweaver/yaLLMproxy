package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/logdb"
)

func main() {
	var cfgPath string
	var requests bool
	var errors bool
	var all bool
	var dryRun bool
	var keepDays int
	flag.StringVar(&cfgPath, "config", "", "config file path")
	flag.BoolVar(&requests, "requests", false, "clean request metadata logs")
	flag.BoolVar(&errors, "errors", false, "clean error logs")
	flag.BoolVar(&all, "all", false, "clean request and error logs")
	flag.BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	flag.IntVar(&keepDays, "keep-days", 0, "only clean logs older than N days; 0 cleans all selected logs")
	flag.Parse()

	if all {
		requests = true
		errors = true
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log database: %v\n", err)
		os.Exit(1)
	}
	if store == nil || !store.Enabled() {
		fmt.Fprintln(os.Stdout, "Log database is not configured; no cleanup needed")
		return
	}
	defer store.Close()
	result, err := store.Cleanup(context.Background(), logdb.CleanupOptions{
		Requests: requests,
		Errors:   errors,
		KeepDays: keepDays,
		DryRun:   dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to clean database: %v\n", err)
		os.Exit(1)
	}
	if dryRun {
		fmt.Fprintln(os.Stdout, "DRY RUN - no changes were made")
	}
	fmt.Fprintf(os.Stdout, "Request logs matched: %d\n", result.RequestsMatched)
	fmt.Fprintf(os.Stdout, "Request logs deleted: %d\n", result.RequestsDeleted)
	fmt.Fprintf(os.Stdout, "Error logs matched: %d\n", result.ErrorsMatched)
	fmt.Fprintf(os.Stdout, "Error logs deleted: %d\n", result.ErrorsDeleted)
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		defaultPath := config.ResolvePath(config.DefaultConfigPath)
		if _, err := os.Stat(defaultPath); errors.Is(err, os.ErrNotExist) {
			return &config.Config{Database: map[string]any{
				"backend": "sqlite",
				"connection": map[string]any{
					"sqlite": map[string]any{"path": "logs/yaLLM.db"},
				},
			}}, nil
		}
	}
	return config.LoadWithOptions(config.LoadOptions{Path: path, SubstituteEnv: true})
}
