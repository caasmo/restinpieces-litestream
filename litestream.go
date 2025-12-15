package litestream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/file"
	"github.com/benbjohnson/litestream/s3"
)

// ConfigScope defines the default scope used when storing/retrieving
// Litestream configuration securely (e.g., in a database).
const ConfigScope = "litestream"



// Litestream handles continuous database backups for potentially multiple replicas.
type Litestream struct {
	store  *litestream.Store // The Store object is now the central orchestrator
	logger *slog.Logger

	// ctx controls the lifecycle of the backup process
	ctx context.Context

	// cancel stops the backup process
	cancel context.CancelFunc

	// shutdownDone signals when backup has completely stopped
	shutdownDone chan struct{}
}

// NewLitestream creates a new Litestream instance configured according to cfg.
// It sets up the database object and initializes all replicas defined in cfg.Replicas.
// The dbPath specifies the database file to back up.
func NewLitestream(dbPath string, cfg Config, logger *slog.Logger) (*Litestream, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("litestream: dbPath cannot be empty")
	}
	if len(cfg.Replicas) == 0 {
		return nil, fmt.Errorf("litestream: no replicas configured")
	}

	ctx, cancel := context.WithCancel(context.Background())

	db := litestream.NewDB(dbPath)        // Use dbPath argument
	db.Logger = logger.With("db", dbPath) // Use dbPath argument
	// Ensure the Replicas slice is initialized before appending
	db.Replicas = make([]*litestream.Replica, 0, len(cfg.Replicas))

	// --- DB-Level settings ---
	if cfg.MonitorInterval != "" {
		d, err := time.ParseDuration(cfg.MonitorInterval)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("litestream: invalid monitor_interval format: %w", err)
		}
		db.MonitorInterval = d
	}
	if cfg.CheckpointInterval != "" {
		d, err := time.ParseDuration(cfg.CheckpointInterval)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("litestream: invalid checkpoint_interval format: %w", err)
		}
		db.CheckpointInterval = d
	}

	// --- Configure Each Replica ---
	for _, rc := range cfg.Replicas {
		if rc.Name == "" {
			cancel()
			return nil, fmt.Errorf("litestream: replica name is required but missing for type '%s'", rc.Type)
		}

		l := logger.With("replica_name", rc.Name, "replica_type", rc.Type)
		var replicaClient litestream.ReplicaClient

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

		// --- Replica-Level Settings ---
		if rc.SyncInterval != "" {
			d, err := time.ParseDuration(rc.SyncInterval)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("litestream: invalid sync_interval format for replica '%s': %w", rc.Name, err)
			}
			replica.SyncInterval = d
		}
		if rc.SnapshotInterval != "" {
			d, err := time.ParseDuration(rc.SnapshotInterval)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("litestream: invalid snapshot_interval format for replica '%s': %w", rc.Name, err)
			}
			replica.SnapshotInterval = d
		}
		if rc.Retention != "" {
			d, err := time.ParseDuration(rc.Retention)
			if err != nil {
				cancel()
				// Note: Litestream's own parsing is more robust here, handling "0" for forever.
				// For simplicity here, we parse duration, assuming non-zero means retain for that long.
				// An empty string "" could also mean forever. Check litestream code if exact behavior is needed.
				return nil, fmt.Errorf("litestream: invalid retention format for replica '%s': %w", rc.Name, err)
			}
			replica.Retention = d
		}

		// Handle Retention="0" or empty string for forever (default behavior)
		if rc.Retention == "" || rc.Retention == "0" {
			replica.Retention = 0 // Explicitly set to 0 duration for "keep forever"
		}

		if rc.RetentionCheckInterval != "" {
			d, err := time.ParseDuration(rc.RetentionCheckInterval)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("litestream: invalid retention_check_interval format for replica '%s': %w", rc.Name, err)
			}
			replica.RetentionCheckInterval = d
		}

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

	// Channel to synchronize startup: reports error or nil for success
	startupComplete := make(chan error, 1)

	go func() {
		var startupErr error // Track the first error encountered

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

		for _, replica := range l.db.Replicas {
			rl := l.logger.With("replica_name", replica.Name) // Replica-specific logger
			rl.Info("💾 litestream: starting replica")
			// replica.Start runs its own goroutine for syncing
			if err := replica.Start(l.ctx); err != nil {
				rl.Error("💾 litestream: CRITICAL - failed to start replica", "error", err)
				startupErr = fmt.Errorf("failed to start replica '%s': %w", replica.Name, err)
				break // Stop trying to start other replicas
			} else {
				rl.Info("💾 litestream: replica started successfully")
			}
		}

		if startupErr != nil {
			l.logger.Error("💾 litestream: one or more replicas failed to start, initiating shutdown", "error", startupErr)
			startupComplete <- startupErr // Report the error back to Start() caller
			l.cancel()                    // Trigger context cancellation to stop everything
			return                        // Exit the goroutine
		}

		l.logger.Info("💾 litestream: all replicas started successfully")
		startupComplete <- nil // Signal successful startup

		<-l.ctx.Done()
		l.logger.Info("💾 litestream: received shutdown signal, initiating replica stop via db.Close()")
		// db.Close() called by defer will handle stopping replicas
	}()

	err := <-startupComplete
	return err
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
