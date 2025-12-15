package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/caasmo/restinpieces"

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

	dbPool, err := restinpieces.NewZombiezenPerformancePool(*dbPath)
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

	// --- Litestream Setup (Load from DB) ---
	var ls *litestream.Litestream // Declare ls variable

	// Proceed with Litestream setup since age key is present.
	app.Logger().Info("Litestream integration enabled")

	// 1. Load Encrypted Config from DB using App's SecureConfigStore
	app.Logger().Info("Loading Litestream configuration from database", "scope", litestream.ConfigScope) // Use exported constant
	encryptedTomlData, err := app.SecureConfigStore().Latest(litestream.ConfigScope)                     // Use exported constant
	if err != nil {
		app.Logger().Error("failed to load Litestream config from DB", "scope", litestream.ConfigScope, "error", err)
		os.Exit(1)
	}
	if len(encryptedTomlData) == 0 {
		app.Logger().Error("Litestream config data loaded from DB is empty", "scope", litestream.ConfigScope)
		os.Exit(1)
	}

	// 2. Unmarshal TOML Config
	var lsCfg litestream.Config
	if err := toml.Unmarshal(encryptedTomlData, &lsCfg); err != nil {
		app.Logger().Error("failed to unmarshal Litestream TOML config", "scope", litestream.ConfigScope, "error", err)
		os.Exit(1)
	}
	// Log without db_path from config, as it's removed
	app.Logger().Info("Successfully unmarshalled Litestream config", "scope", litestream.ConfigScope, "replica_count", len(lsCfg.Replicas))

	app.Logger().Info("Litestream integration enabled", "conf", lsCfg)
	// 4. Instantiate Litestream
	ls, err = litestream.NewLitestream(*dbPath, lsCfg, app.Logger())
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
