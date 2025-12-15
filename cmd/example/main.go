package main

import (
	"bytes"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/caasmo/restinpieces"

	lsconfig "github.com/benbjohnson/litestream/config"
	"github.com/caasmo/restinpieces-litestream"
)

func main() {
	// Define flags directly in main
	dbPath := flag.String("dbpath", "", "Path to the SQLite database file (required)")
	ageKeyPath := flag.String("age-key", "", "Path to the age identity (private key) file (required)")

	// Set custom usage message for the application
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -dbpath <database-path> -age-key <identity-file-path>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Start the restinpieces application server.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	// Parse flags
	flag.Parse()

	// Validate required flags
	if *dbPath == "" || *ageKeyPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	dbPool, err := restinpieces.NewZombiezenPool(*dbPath)
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
	    os.Exit(1)
	}

	defer func() {
		slog.Info("Closing database pool...")
		if err := dbPool.Close(); err != nil {
			slog.Error("Error closing database pool", "error", err)
		}
	}()

	app, srv, err := restinpieces.New(
		restinpieces.WithZombiezenPool(dbPool), 
		restinpieces.WithAgeKeyPath(*ageKeyPath),
        // use default cache ristretto
        // use default router serveMux
        // use default slog logger Text
	)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	// --- Litestream Setup ---
	// The configuration is now a standard Litestream YAML file.
	var ls *litestream.Litestream 

	app.Logger().Info("Litestream integration enabled")

	// 1. Load Encrypted Config from DB using App's SecureConfigStore
	app.Logger().Info("Loading Litestream configuration from database", "scope", litestream.ConfigScope) 
	configData, err := app.ConfigStore().Get(litestream.ConfigScope, 0)
	if err != nil {
		app.Logger().Error("failed to load Litestream config from DB", "scope", litestream.ConfigScope, "error", err)
		os.Exit(1)
	}
	if len(configData) == 0 {
		app.Logger().Error("Litestream config data loaded from DB is empty", "scope", litestream.ConfigScope)
		os.Exit(1)
	}

	// 2. Parse and Validate Litestream Config
	app.Logger().Info("Parsing and validating Litestream configuration")
	lsCfg, err := lsconfig.ParseConfig(bytes.NewReader(configData), false)
	if err != nil {
		app.Logger().Error("invalid litestream config", "scope", litestream.ConfigScope, "error", err)
		os.Exit(1)
	}
	app.Logger().Info("Successfully parsed Litestream config")

	app.Logger().Info("Litestream integration enabled")
	// 4. Instantiate Litestream
	ls, err = litestream.NewLitestream(&lsCfg, app.Logger())
	if err != nil {
		app.Logger().Error("failed to init Litestream", "error", err)
		os.Exit(1)
	}

	// 5. Add Litestream as a Daemon
	srv.AddDaemon(ls)
	app.Logger().Info("Litestream daemon added to the server")

	srv.Run()

	slog.Info("Server shut down gracefully.")
}
