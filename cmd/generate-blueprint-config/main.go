package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/caasmo/restinpieces-litestream"
)

// generateBlueprintConfig creates a Litestream configuration struct
// populated with example/dummy data.
func generateBlueprintConfig() litestream.Config {
	// Define example replicas
	replicas := []litestream.ReplicaConfig{
		{
			Name:     "local_file_example", // Unique name for this replica
			Type:     "file",
			FilePath: "/path/to/your/local/replicas", // Placeholder: Local directory for backup
		},
		{
			Name:              "s3_backup_example", // Unique name for the S3 replica
			Type:              "s3",
			S3Bucket:          "your-s3-bucket-name",    // Placeholder: Your S3 bucket name
			S3Region:          "your-s3-region",         // Placeholder: Your S3 bucket region
			S3Path:            "backups/myapp",          // Optional: Path prefix in the bucket
			S3Endpoint:        "endpoint",               // Optional: Use for S3-compatible storage (e.g., MinIO URL)
			S3AccessKeyID:     "YOUR_ACCESS_KEY_ID",     // Placeholder: Set via env or secrets management
			S3SecretAccessKey: "YOUR_SECRET_ACCESS_KEY", // Placeholder: Set via env or secrets management
			S3ForcePathStyle:  false,                    // Set to true for MinIO or other S3-compatibles
			// S3SkipVerify:   false, // Set to true if using self-signed certs (use with caution)
		},
		// Add more replica examples if needed
	}

	// Create the main config struct (DBPath is removed)
	cfg := litestream.Config{
		Replicas: replicas,
	}

	return cfg
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	outputFileFlag := flag.String("output", "litestream.blueprint.toml", "Output file path for the blueprint TOML configuration")
	flag.StringVar(outputFileFlag, "o", "litestream.blueprint.toml", "Output file path (shorthand)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Generates a blueprint Litestream TOML configuration file with example values.\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	logger.Info("Generating Litestream blueprint configuration...")
	blueprintCfg := generateBlueprintConfig()

	logger.Info("Marshalling configuration to TOML...")
	tomlBytes, err := toml.Marshal(blueprintCfg)
	if err != nil {
		logger.Error("Failed to marshal blueprint config to TOML", "error", err)
		os.Exit(1)
	}

	logger.Info("Writing blueprint configuration", "path", *outputFileFlag)
	err = os.WriteFile(*outputFileFlag, tomlBytes, 0644)
	if err != nil {
		logger.Error("Failed to write blueprint config file",
			"path", *outputFileFlag,
			"error", err)
		os.Exit(1)
	}

	logger.Info("Litestream blueprint configuration generated successfully", "path", *outputFileFlag)
}
