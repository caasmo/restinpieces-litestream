package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/caasmo/restinpieces"
	"github.com/caasmo/restinpieces-litestream"
)

// Pool creation helpers moved to restinpieces package

func main() {
	// Define flags directly in main
	dbfile := flag.String("dbfile", "app.db", "SQLite database file path")
	configFile := flag.String("config", "", "Path to configuration file")

	// Set custom usage message for the application
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Start the restinpieces application server.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	// Parse flags
	flag.Parse()

	// --- Create the Database Pool ---
	// Use the helper from the library to create a pool with suitable defaults.
	//dbPool, err := restinpieces.NewCrawshawPool(*dbfile)
	dbPool, err := restinpieces.NewZombiezenPool(*dbfile)
	if err != nil {
		slog.Error("failed to create database pool", "error", err)
		os.Exit(1) // Exit if pool creation fails
	}
	// Defer closing the pool here, as main owns it now.
	// This must happen *after* the server finishes.
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
		//restinpieces.WithDbCrawshaw(dbPool),
		restinpieces.WithRouterServeMux(),
		restinpieces.WithCacheRistretto(),
		restinpieces.WithTextLogger(nil),
	)
	if err != nil {
		slog.Error("failed to initialize application", "error", err)
		os.Exit(1) 
	}

	// --- litestream ---
    lsCfg := litestream.Config{
        DBPath:      *dbfile, // Still use DBPath from main config
        ReplicaPath: "./litestream_replicas", // Ad-hoc path
        ReplicaName: "main-db-backup",        // Ad-hoc name
    }

    ls, err := litestream.NewLitestream(lsCfg, app.Logger())
    if err != nil {
        // Log the error and decide if it's fatal
        app.Logger().Error("failed to initialize litestream", "error", err)
		os.Exit(1) 

    }

	srv.AddDaemon(ls)

	// Start the server
	// The Run method blocks until the server stops (e.g., via signal)
	srv.Run()

	slog.Info("Server shut down gracefully.")
	// No explicit os.Exit(0) needed, successful completion implies exit 0
}
