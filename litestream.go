package litestream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/file"
	"github.com/benbjohnson/litestream/s3" // Import S3 package
)

// ReplicaConfig holds configuration for a single Litestream replica.
type ReplicaConfig struct {
	Name string // REQUIRED, unique name for this replica (e.g., "local", "s3-main")
	Type string // Replica type: "file" or "s3"

	// --- File Replica Settings ---
	FilePath string // Directory path for storing file replicas (used if Type == "file")

	// --- S3 Replica Settings ---
	S3Endpoint        string // S3 API endpoint (e.g., "s3.amazonaws.com" or MinIO address)
	S3Region          string // S3 region (e.g., "us-east-1")
	S3Bucket          string // S3 bucket name
	S3Path            string // Optional path prefix within the bucket
	S3AccessKeyID     string // S3 Access Key ID
	S3SecretAccessKey string // S3 Secret Access Key
	S3ForcePathStyle  bool   // Use path-style addressing (needed for MinIO/S3-compatibles)
	// S3SkipVerify    bool   // Optional: Skip TLS verification
}

// Config holds the main Litestream configuration, including the database path
// and a list of replicas.
type Config struct {
	DBPath   string          // Path to the database file to be backed up.
	Replicas []ReplicaConfig // Slice defining one or more replicas.
}

// Litestream handles continuous database backups for potentially multiple replicas.
type Litestream struct {
	config Config // Store the main config
	logger *slog.Logger
	db     *litestream.DB // The DB object holds the list of replicas internally

	// ctx controls the lifecycle of the backup process for all replicas
	ctx context.Context

	// cancel stops the backup process
	cancel context.CancelFunc

	// shutdownDone signals when backup has completely stopped
	shutdownDone chan struct{}
}

// NewLitestream creates a new Litestream instance configured according to cfg.
// It sets up the database object and initializes all replicas defined in cfg.Replicas.
func NewLitestream(cfg Config, logger *slog.Logger) (*Litestream, error) {
	if len(cfg.Replicas) == 0 {
		return nil, fmt.Errorf("litestream: no replicas configured")
	}

	ctx, cancel := context.WithCancel(context.Background())

	db := litestream.NewDB(cfg.DBPath)
	db.Logger = logger.With("db", cfg.DBPath)
	// Ensure the Replicas slice is initialized before appending
	db.Replicas = make([]*litestream.Replica, 0, len(cfg.Replicas))

	// --- Configure Each Replica ---
	for _, rc := range cfg.Replicas {
		if rc.Name == "" {
			cancel()
			return nil, fmt.Errorf("litestream: replica name is required but missing for type '%s'", rc.Type)
		}

		l := logger.With("replica_name", rc.Name, "replica_type", rc.Type)
		var replicaClient litestream.ReplicaClient
		var err error

		switch rc.Type {
		case "file":
			if rc.FilePath == "" {
				cancel()
				return nil, fmt.Errorf("litestream: FilePath is required for file replica '%s'", rc.Name)
			}
			if err := os.MkdirAll(rc.FilePath, 0750); err != nil && !os.IsExist(err) {
				cancel()
				return nil, fmt.Errorf("litestream: failed to create file replica directory '%s' for replica '%s': %w", rc.FilePath, rc.Name, err)
			}
			absFilePath, err := filepath.Abs(rc.FilePath)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("litestream: failed to get absolute path for file replica '%s' path '%s': %w", rc.Name, rc.FilePath, err)
			}
			replicaClient = file.NewReplicaClient(absFilePath)
			l.Info("Configured file replica client", "path", absFilePath)

		case "s3":
			if rc.S3Bucket == "" || rc.S3Region == "" { // Basic validation
				cancel()
				return nil, fmt.Errorf("litestream: S3Bucket and S3Region are required for S3 replica '%s'", rc.Name)
			}
			s3Client := s3.NewReplicaClient()
			s3Client.Bucket = rc.S3Bucket
			s3Client.Path = rc.S3Path
			s3Client.Region = rc.S3Region
			s3Client.Endpoint = rc.S3Endpoint
			s3Client.AccessKeyID = rc.S3AccessKeyID
			s3Client.SecretAccessKey = rc.S3SecretAccessKey
			s3Client.ForcePathStyle = rc.S3ForcePathStyle
			// s3Client.SkipVerify = rc.S3SkipVerify // Add if needed

			replicaClient = s3Client
			l.Info("Configured S3 replica client", "endpoint", rc.S3Endpoint, "bucket", rc.S3Bucket, "path", rc.S3Path, "region", rc.S3Region)

		default:
			cancel()
			return nil, fmt.Errorf("litestream: unsupported replica type '%s' for replica '%s'", rc.Type, rc.Name)
		}

		// Create the replica object and link it to the DB
		replica := litestream.NewReplica(db, rc.Name)
		replica.Client = replicaClient
		db.Replicas = append(db.Replicas, replica)
	}

	return &Litestream{
		config:       cfg,
		logger:       logger,
		db:           db, // DB now holds the configured replicas
		ctx:          ctx,
		cancel:       cancel,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Name returns the name of the service for logging and identification.
func (l *Litestream) Name() string {
	return "LitestreamBackup"
}

// Start begins the continuous backup process in a goroutine.
// It returns an error immediately if the initial setup (opening the database
// or starting the replica) fails. Otherwise, it returns nil and the backup
// process continues in the background. Any errors during individual replica
// startup within the goroutine will be logged but won't stop the process.
func (l *Litestream) Start() error {
	l.logger.Info("💾 litestream: opening database for replication")
	// Open database - this is the primary blocking operation before the goroutine.
	if err := l.db.Open(); err != nil {
		l.logger.Error("💾 litestream: failed to open database", "error", err)
		return fmt.Errorf("litestream: failed to open database: %w", err)
	}
	l.logger.Info("💾 litestream: database opened successfully")

	go func() {
		defer close(l.shutdownDone)
		defer func() {
			l.logger.Info("💾 litestream: closing database")
			if err := l.db.Close(); err != nil {
				l.logger.Error("💾 litestream: error closing database during shutdown", "error", err)
			} else {
				l.logger.Debug("💾 litestream: database closed")
			}
		}()

		l.logger.Info("💾 litestream: starting replication for all configured replicas")

		// Start replication for each replica.
		// Errors here are logged, but don't stop other replicas from trying.
		for _, replica := range l.db.Replicas {
			l := l.logger.With("replica_name", replica.Name)
			l.Info("💾 litestream: starting replica")
			// replica.Start runs its own goroutine for syncing
			if err := replica.Start(l.ctx); err != nil {
				// Log critical error, but don't stop the main loop
				l.Error("💾 litestream: failed to start replica", "error", err)
			} else {
				l.Info("💾 litestream: replica started successfully")
			}
		}

		l.logger.Info("💾 litestream: all replica start attempts initiated")

		// Wait for shutdown signal
		<-l.ctx.Done()
		l.logger.Info("💾 litestream: received shutdown signal, initiating replica stop via db.Close()")
		// db.Close() called by defer will handle stopping replicas
	}()

	return nil // Return nil as db.Open() succeeded
}

// Stop gracefully shuts down the backup process by cancelling the context.
// It waits until the background goroutine confirms shutdown or the provided context times out.
func (l *Litestream) Stop(ctx context.Context) error {
	l.logger.Info("💾 litestream: stopping backup process")
	l.cancel() // Signal the background goroutine to stop

	select {
	case <-l.shutdownDone:
		l.logger.Info("💾 litestream: stopped gracefully")
		return nil
	case <-ctx.Done():
		l.logger.Info("💾 litestream: shutdown timed out")
		return ctx.Err()
	}
}
