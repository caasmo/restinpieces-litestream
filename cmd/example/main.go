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
// This function is the central place for defining the Litestream configuration,
// including the database path and all desired replicas.
func getConf(dbPath string) litestream.Config {
	replicas := []litestream.ReplicaConfig{
		{
			Name:     "local_file", // Unique name for this replica
			Type:     "file",
			FilePath: "./litestream_replicas", // Local directory for backup
		},
		{
			Name:              "s3_backup", // Unique name for the S3 replica
			Type:              "s3",
			S3Bucket:          "my-litestream-test-bucket", // CHANGE: Your S3 bucket name
			S3Region:          "us-east-1",                 // CHANGE: Your S3 bucket region
			S3Path:            "backups/myapp",             // Optional: Path prefix in the bucket
			S3Endpoint:        "",                          // Optional: Use for S3-compatible storage (e.g., MinIO URL like "http://localhost:9000")
			S3AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"), // Read from environment
			S3SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), // Read from environment
			S3ForcePathStyle:  false, // Set to true for MinIO or other S3-compatibles requiring path-style access
			// S3SkipVerify:   true, // Uncomment if using self-signed certs (use with caution)
		},
	}

	// Log which replicas are being configured (optional)
	for _, r := range replicas {
		slog.Info("Configuring Litestream replica", "name", r.Name, "type", r.Type)
		if r.Type == "s3" {
			slog.Info("S3 Replica Details", "bucket", r.S3Bucket, "region", r.S3Region, "path", r.S3Path, "endpoint", r.S3Endpoint)
			if r.S3AccessKeyID == "" || r.S3SecretAccessKey == "" {
				slog.Warn("S3 credentials not found in environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY). S3 replica might fail.")
			}
		}
	}


	return litestream.Config{
		DBPath:   dbPath,
		Replicas: replicas,
	}
}
