package litestream

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"

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
	"github.com/caasmo/restinpieces"
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

// New creates a new Litestream daemon by loading its configuration from the
// provided restinpieces application context.
func New(app *restinpieces.App) (*Litestream, error) {
	logger := app.Logger()

	logger.Info("Loading Litestream configuration from database", "scope", ConfigScope)
	configData, format, err := app.ConfigStore().Get(ConfigScope, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to load Litestream config from DB: %w", err)
	}
	if len(configData) == 0 {
		return nil, fmt.Errorf("litestream config data loaded from DB is empty (scope: %s)", ConfigScope)
	}
	if format != "yaml" {
		return nil, fmt.Errorf("invalid litestream config format: expected 'yaml', found '%s'", format)
	}

	logger.Info("Parsing and validating Litestream configuration")
	cfg, err := config.ParseConfig(bytes.NewReader(configData), false)
	if err != nil {
		return nil, fmt.Errorf("invalid litestream config: %w", err)
	}
	logger.Info("Successfully parsed Litestream config")

	// Configure Litestream's internal (global) logger.
	// This directs Litestream's core logs to stderr, with the level and format
	// specified in the config file. This does not affect the main framework logger.
	if cfg.Logging != nil {
		config.InitLog(os.Stderr, cfg.Logging.Level, cfg.Logging.Type)
	}

	// Use the internal setup function to create the daemon instance
	return setup(&cfg, logger)
}

// setup creates a new Litestream instance from a configuration object.
func setup(cfg *config.Config, logger *slog.Logger) (*Litestream, error) {
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
// It returns an error if the initial setup fails, otherwise it returns nil
// and the backup process continues in the background.
func (l *Litestream) Start() error {
	l.logger.Info("starting litestream backup service")
	startupComplete := make(chan error, 1)

	go func() {
		defer close(l.shutdownDone)
		defer func() {
			// Ensure all monitors are closed first.
			for _, m := range l.directoryMonitors {
				m.Close()
			}
			l.logger.Debug("closed all directory monitors")

			// Then close the store, which handles its databases.
			if err := l.store.Close(context.Background()); err != nil {
				l.logger.Error("failed to close store during shutdown", "error", err)
			} else {
				l.logger.Debug("litestream store closed")
			}
		}()

		// Open the store, which begins monitoring all its databases.
		if err := l.store.Open(l.ctx); err != nil {
			l.logger.Error("cannot open store", "error", err)
			startupComplete <- err
			return
		}
		l.logger.Info("litestream store opened")

		// Start directory monitors for dynamic database discovery.
		for _, entry := range l.watchables {
			monitor, err := setup.NewDirectoryMonitor(l.ctx, l.store, entry.config, entry.dbs)
			if err != nil {
				l.logger.Error("failed to start directory monitor, shutting down", "dir", entry.config.Dir, "error", err)
				// A failure to start a monitor is critical, trigger a shutdown.
				l.cancel()
				startupComplete <- err
				return
			}
			l.directoryMonitors = append(l.directoryMonitors, monitor)
			l.logger.Info("started directory monitor", "dir", entry.config.Dir)
		}

		l.logger.Info("litestream backup service started successfully")
		startupComplete <- nil // Signal successful startup

		// Wait for shutdown signal.
		<-l.ctx.Done()
		l.logger.Info("received shutdown signal")
	}()

	// Wait for the startup to complete or fail.
	return <-startupComplete
}

// Stop gracefully shuts down the backup process by cancelling the context.
// It waits until the background goroutine confirms shutdown or the provided context times out.
func (l *Litestream) Stop(ctx context.Context) error {
	l.logger.Info("stopping litestream backup service")
	l.cancel() // Signal the background goroutine to stop

	select {
	case <-l.shutdownDone:
		l.logger.Info("litestream backup service stopped gracefully")
		return nil
	case <-ctx.Done():
		l.logger.Warn("shutdown timed out", "error", ctx.Err())
		return ctx.Err()
	}
}
