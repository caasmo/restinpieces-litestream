package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"context"

	"github.com/caasmo/restinpieces"
	"strings" // <-- Import strings package

	"github.com/caasmo/restinpieces-litestream"
)

// --- Custom Log Handler for Litestream Filtering ---

// LitestreamLogFilter wraps a slog.Handler to filter Litestream debug messages.
type LitestreamLogFilter struct {
	next slog.Handler // The next handler in the chain
}

// NewLitestreamLogFilter creates a new filtering handler.
func NewLitestreamLogFilter(next slog.Handler) *LitestreamLogFilter {
	return &LitestreamLogFilter{next: next}
}

// Enabled implements slog.Handler. It checks if the level is enabled by the next handler.
// We don't filter based on level here, Handle does the filtering.
func (h *LitestreamLogFilter) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler. It filters DEBUG messages unless they contain specific keywords.
func (h *LitestreamLogFilter) Handle(ctx context.Context, r slog.Record) error {
	// Allow non-DEBUG messages unconditionally
	if r.Level >= slog.LevelInfo {
		return h.next.Handle(ctx, r)
	}

	// For DEBUG messages, check the message content
	if r.Level == slog.LevelDebug {
		// Allow specific DEBUG messages (e.g., indicating a file copy)
		if strings.Contains(r.Message, "copy-shadow") {
			return h.next.Handle(ctx, r)
		}
		// Discard other DEBUG messages by returning nil (no error, but don't handle)
		return nil
	}

	// Handle any other levels normally (though shouldn't happen with standard levels)
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *LitestreamLogFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewLitestreamLogFilter(h.next.WithAttrs(attrs))
}

// WithGroup implements slog.Handler.
func (h *LitestreamLogFilter) WithGroup(name string) slog.Handler {
	return NewLitestreamLogFilter(h.next.WithGroup(name))
}

// --- End Custom Log Handler ---


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

    // Wrap the application logger with the filter
    filteredLogger := slog.New(NewLitestreamLogFilter(app.Logger().Handler()))

    ls, err := litestream.NewLitestream(lsCfg, filteredLogger) // Pass the filtered logger
    if err != nil {
        // Log the error and decide if it's fatal
        filteredLogger.Error("failed to initialize litestream", "error", err) // Use filtered logger here too
		os.Exit(1) 

    }

	srv.AddDaemon(ls)

	// Start the server
	// The Run method blocks until the server stops (e.g., via signal)
	srv.Run()

	slog.Info("Server shut down gracefully.")
	// No explicit os.Exit(0) needed, successful completion implies exit 0
}
