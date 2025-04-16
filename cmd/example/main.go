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

	// --- Litestream Flags ---
	// Note: dbfile is reused for Litestream's DBPath
	lsReplicaPath := flag.String("litestream-replica-path", "./litestream_replicas", "Directory path for storing Litestream replicas")
	lsReplicaName := flag.String("litestream-replica-name", "main-db-backup", "Unique identifier for this Litestream replica instance")
	// Add more flags here to control other litestream.ReplicaConfig options if needed

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Start the restinpieces application server.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// --- Get Litestream Configuration ---
	lsCfg := getConf(*dbfile, *lsReplicaPath, *lsReplicaName)

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

	// --- litestream ---
    lsCfg := litestream.Config{
        DBPath:      *dbfile, // Still use DBPath from main config
        ReplicaPath: "./litestream_replicas", // Ad-hoc path
        ReplicaName: "main-db-backup",        // Ad-hoc name
    }

    ls, err := litestream.NewLitestream(lsCfg, app.Logger()) 
    if err != nil {
		os.Exit(1) 

    }

	srv.AddDaemon(ls)

	// Start the server
	srv.Run()

	slog.Info("Server shut down gracefully.")
}

// getConf reads litestream configuration options from command-line flags
// and returns a populated litestream.Config struct.
func getConf(dbPath, replicaPath, replicaName string) litestream.Config {
	// Basic configuration from flags
	cfg := litestream.Config{
		DBPath:      dbPath,      // Use the main dbfile flag
		ReplicaPath: replicaPath, // From --litestream-replica-path flag
		ReplicaName: replicaName, // From --litestream-replica-name flag
	}

	// Here you could add logic to read more advanced options
	// from flags or a config file if needed, for example:
	// cfg.ReplicaConfig.Retention = *lsRetentionFlag
	// cfg.ReplicaConfig.SyncInterval = *lsSyncIntervalFlag
	// etc.

	slog.Info("Litestream Configuration",
		"DBPath", cfg.DBPath,
		"ReplicaPath", cfg.ReplicaPath,
		"ReplicaName", cfg.ReplicaName,
	)

	return cfg
}
