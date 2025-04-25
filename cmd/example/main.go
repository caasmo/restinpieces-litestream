package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/caasmo/restinpieces"

	"github.com/caasmo/restinpieces-litestream"
)

func main() {
	// --- Core Application Flags ---
	dbfile := flag.String("dbfile", "app.db", "SQLite database file path")
	configFile := flag.String("config", "", "Path to configuration file")

	// Litestream configuration is now managed within the getConf function.

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Start the restinpieces application server.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

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
		*configFile,
		restinpieces.WithDbZombiezen(dbPool),
		restinpieces.WithRouterServeMux(),
		restinpieces.WithCacheRistretto(),
		restinpieces.WithTextLogger(nil),
	)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	// --- Litestream setup removed from example ---
	// Litestream configuration and initialization are no longer part of this example.
	// Use the 'generate-blueprint-config' tool to create a config file,
	// and potentially another tool or manual process to load and run Litestream based on that config.

	// Start the server
	srv.Run()

	slog.Info("Server shut down gracefully.")
}
