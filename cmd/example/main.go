package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/pelletier/go-toml/v2" // Added for TOML unmarshalling

	"github.com/caasmo/restinpieces"
	"github.com/caasmo/restinpieces/config" // Added for SecureConfig
	dbz "github.com/caasmo/restinpieces/db/zombiezen" // Added for DB implementation access

	"github.com/caasmo/restinpieces-litestream"
)

// Define a scope constant for Litestream config
const litestreamConfigScope = "litestream"

func main() {
	// --- Core Application Flags ---
	dbfile := flag.String("dbfile", "app.db", "SQLite database file path")
	ageKeyPath := flag.String("age-key", "", "Path to the age identity file (private key) for decrypting Litestream config (required if using Litestream)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -dbfile <path> -age-key <path> [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Start the restinpieces application server.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *ageKeyPath == "" {
		app.Logger().Error("Missing required flag: -age-key is needed for Litestream configuration")
		flag.Usage() // Show usage instructions
		os.Exit(1)
	}

	// --- Create the Database Pool ---
	// Use the helper from the library to create a pool with suitable defaults.
	dbPool, err := restinpieces.NewZombiezenPool(*dbfile)
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1) // Exit if pool creation fails
	}

	defer func() {
		slog.Info("Closing database pool...")
		if err := dbPool.Close(); err != nil {
			slog.Error("Error closing database pool", "error", err)
		}
	}()

	// --- Initialize the Application ---
	app, srv, err := restinpieces.New(
		restinpieces.WithDbZombiezen(dbPool),
		restinpieces.WithAgeKeyPath(*ageKeyPath),
		restinpieces.WithRouterServeMux(),
		restinpieces.WithCacheRistretto(),
		restinpieces.WithTextLogger(nil),
	)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	// --- Litestream Setup (Load from DB) ---
	var ls *litestream.Litestream // Declare ls variable


	// Proceed with Litestream setup since age key is present.
	app.Logger().Info("Litestream integration enabled via -age-key flag")

	// 1. Get DB implementation (needed for SecureConfig)
	// We assume the Zombiezen DB is used as configured above.
	// In a real app, you might need a more robust way to get the DB interface.
	dbImpl, err := dbz.New(dbPool) // Create a new instance for SecureConfig
	if err != nil {
		app.Logger().Error("failed to instantiate zombiezen db for secure config", "error", err)
		os.Exit(1)
	}

	// 2. Instantiate SecureConfig
	secureCfg, err := config.NewSecureConfigAge(dbImpl, *ageKeyPathFlag, app.Logger())
	if err != nil {
		app.Logger().Error("failed to instantiate secure config (age) for Litestream", "error", err)
		os.Exit(1)
	}

	// 3. Load Encrypted Config from DB
	app.Logger().Info("Loading Litestream configuration from database", "scope", *litestreamScopeFlag)
	encryptedTomlData, err := secureCfg.Latest(*litestreamScopeFlag)
	if err != nil {
		app.Logger().Error("failed to load Litestream config from DB", "scope", *litestreamScopeFlag, "error", err)
		// Decide if this is fatal. Maybe Litestream is optional? For this example, we exit.
		os.Exit(1)
	}
	if len(encryptedTomlData) == 0 {
		app.Logger().Error("Litestream config data loaded from DB is empty", "scope", *litestreamScopeFlag)
		os.Exit(1) // Exit if config is empty
	}

	// 4. Unmarshal TOML Config
	var lsCfg litestream.Config
	if err := toml.Unmarshal(encryptedTomlData, &lsCfg); err != nil {
		app.Logger().Error("failed to unmarshal Litestream TOML config", "scope", *litestreamScopeFlag, "error", err)
		os.Exit(1)
	}
	app.Logger().Info("Successfully unmarshalled Litestream config", "scope", *litestreamScopeFlag, "db_path", lsCfg.DBPath, "replica_count", len(lsCfg.Replicas))

	// Ensure the DB path in the Litestream config matches the main app DB path
	if lsCfg.DBPath != *dbfile {
		app.Logger().Warn("Litestream config DB path differs from application DB path",
		"litestream_db_path", lsCfg.DBPath,
		"app_db_path", *dbfile)
		// Optionally override or exit based on policy. Here we override.
		app.Logger().Info("Overriding Litestream DB path with application DB path", "new_path", *dbfile)
		lsCfg.DBPath = *dbfile
	}


	// 5. Instantiate Litestream
	ls, err = litestream.NewLitestream(lsCfg, app.Logger())
	if err != nil {
		// Error logged within NewLitestream
		os.Exit(1)
	}

	// 6. Add Litestream as a Daemon
	srv.AddDaemon(ls)
	app.Logger().Info("Litestream daemon added to the server")
	// End of Litestream setup block (no 'else' needed anymore)

	// Start the server (which will also start Litestream if added)
	srv.Run()

	slog.Info("Server shut down gracefully.")
}
