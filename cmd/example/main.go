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

	// --- Get Litestream Configuration ---
	// dbfile is still needed as it's fundamental to the backup target.
	lsCfg := getConf(*dbfile)

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
	// Configuration is obtained from getConf above.
	ls, err := litestream.NewLitestream(lsCfg, app.Logger())
	if err != nil {
		// Error is logged within NewLitestream if it fails
		os.Exit(1)

    }

	srv.AddDaemon(ls)

	// Start the server
	srv.Run()

	slog.Info("Server shut down gracefully.")
}

// getConf provides the configuration for Litestream.
// Currently, it uses hardcoded values for replica settings but takes the
// database path as an argument.
// This function is intended to be the central place for managing all
// Litestream settings (e.g., replica type, retention, sync intervals)
// once the litestream package is refactored to support them.
func getConf(dbPath string) litestream.Config {
	// Define Litestream configuration here.
	// In a future refactor, this could return a more comprehensive struct
	// mirroring litestream.DBConfig and litestream.ReplicaConfig.
	cfg := litestream.Config{
		// DBPath is derived from the --dbfile flag passed to the application.
		DBPath: dbPath,

		// ReplicaPath specifies the directory where Litestream will store
		// its backup files (snapshots and WAL segments).
		// Default: "./litestream_replicas" (relative to the working directory).
		// Consider using an absolute path or a path derived from configuration
		// for production deployments.
		ReplicaPath: "./litestream_replicas",

		// ReplicaName provides a unique identifier for this specific replica.
		// This is important if you have multiple replicas (e.g., different
		// backup targets or instances).
		// Default: "default-backup".
		ReplicaName: "default-backup",

		// --- Future Configuration Examples ---
		// These fields are not yet used by the current litestream.go implementation
		// but demonstrate how this function could be extended.
		//
		// ReplicaType: "file", // Or "s3", "abs", "gcs", etc.
		// AccessKeyID: "...", // For S3/compatible
		// SecretAccessKey: "...", // For S3/compatible
		// Region: "us-east-1", // For S3/compatible
		// Bucket: "my-backup-bucket", // For S3/compatible
		// Path: "database_backups/", // Path within the bucket
		// Retention: "24h", // How long to keep snapshots/WALs
		// SyncInterval: "1s", // How often to sync WAL changes
	}

	// No logging here; configuration is static within this function.
	// Logging can happen where NewLitestream is called if needed.

	return cfg
}
