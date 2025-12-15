package litestream

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/abs"
	"github.com/benbjohnson/litestream/config"
	"github.com/benbjohnson/litestream/file"
	"github.com/benbjohnson/litestream/gs"
	"github.com/benbjohnson/litestream/nats"
	"github.com/benbjohnson/litestream/oss"
	"github.com/benbjohnson/litestream/s3"
	"github.com/benbjohnson/litestream/sftp"
	"github.com/benbjohnson/litestream/setup"
)

// ConfigScope defines the default scope used when storing/retrieving
// Litestream configuration securely (e.g., in a database).
const ConfigScope = "litestream"

// watchable is a helper struct to hold information about database directories
// that need to be monitored for changes.
type watchable struct {
	config *config.DBConfig
	dbs    []*litestream.DB
}

// Litestream handles continuous database backups for potentially multiple replicas.
type Litestream struct {
	store  *litestream.Store
	logger *slog.Logger

	// Daemon lifecycle management, required by the restinpieces framework.
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownDone chan struct{}

	// Information to start directory monitors, populated in New() and consumed by Start().
	watchables []watchable

	// Holds the running directory monitors so they can be closed by Stop().
	directoryMonitors []*setup.DirectoryMonitor
}

// NewLitestream creates a new Litestream instance from a configuration object.
func NewLitestream(cfg *config.Config, logger *slog.Logger) (*Litestream, error) {
	// Setup databases.
	if len(cfg.DBs) == 0 {
		return nil, fmt.Errorf("no databases specified in configuration")
	}

	var dbs []*litestream.DB
	var watchables []watchable
	for _, dbConfig := range cfg.DBs {
		// Handle directory configuration
		if dbConfig.Dir != "" {
			dirDbs, err := setup.NewDBsFromDirectoryConfig(dbConfig)
			if err != nil {
				return nil, err
			}
			dbs = append(dbs, dirDbs...)
			logger.Info("found databases in directory", "dir", dbConfig.Dir, "count", len(dirDbs), "watch", dbConfig.Watch)
			if dbConfig.Watch {
				watchables = append(watchables, watchable{config: dbConfig, dbs: dirDbs})
			}
		} else {
			// Handle single database configuration
			db, err := setup.NewDBFromConfig(dbConfig)
			if err != nil {
				return nil, err
			}
			dbs = append(dbs, db)
		}
	}

	levels := cfg.CompactionLevels()
	store := litestream.NewStore(dbs, levels)
	// Only override default snapshot interval if explicitly set in config
	if cfg.Snapshot.Interval != nil {
		store.SnapshotInterval = *cfg.Snapshot.Interval
	}
	// Only override default snapshot retention if explicitly set in config
	if cfg.Snapshot.Retention != nil {
		store.SnapshotRetention = *cfg.Snapshot.Retention
	}
	if cfg.L0Retention != nil {
		store.SetL0Retention(*cfg.L0Retention)
	}
	if cfg.L0RetentionCheckInterval != nil {
		store.L0RetentionCheckInterval = *cfg.L0RetentionCheckInterval
	}

	// Notify user that initialization is done.
	for _, db := range store.DBs() {
		r := db.Replica
		logger.Info("initialized db", "path", db.Path())
		slogWith := logger.With("type", r.Client.Type(), "sync-interval", r.SyncInterval)
		switch client := r.Client.(type) {
		case *file.ReplicaClient:
			slogWith.Info("replicating to", "path", client.Path())
		case *s3.ReplicaClient:
			slogWith.Info("replicating to", "bucket", client.Bucket, "path", client.Path, "region", client.Region, "endpoint", client.Endpoint)
		case *gs.ReplicaClient:
			slogWith.Info("replicating to", "bucket", client.Bucket, "path", client.Path)
		case *abs.ReplicaClient:
			slogWith.Info("replicating to", "bucket", client.Bucket, "path", client.Path, "endpoint", client.Endpoint)
		case *sftp.ReplicaClient:
			slogWith.Info("replicating to", "host", client.Host, "user", client.User, "path", client.Path)
		case *nats.ReplicaClient:
			slogWith.Info("replicating to", "bucket", client.BucketName, "url", client.URL)
		case *oss.ReplicaClient:
			slogWith.Info("replicating to", "bucket", client.Bucket, "path", client.Path, "region", client.Region)
		default:
			slogWith.Info("replicating to")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Litestream{
		store:             store,
		logger:            logger,
		ctx:               ctx,
		cancel:            cancel,
		shutdownDone:      make(chan struct{}),
		watchables:        watchables,
		directoryMonitors: make([]*setup.DirectoryMonitor, 0),
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
