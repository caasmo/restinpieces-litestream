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
	slog.Info("Initializing Litestream daemon...")
	var ls *litestream.Litestream
	ls, err = litestream.New(app)
	if err != nil {
		slog.Error("failed to init litestream", "error", err)
		os.Exit(1)
	}

	// 5. Add Litestream as a Daemon
	srv.AddDaemon(ls)
	slog.Info("Litestream daemon added to the server")

	srv.Run()

	slog.Info("Server shut down gracefully.")
}
