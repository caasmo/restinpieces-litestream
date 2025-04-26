# restinpieces-litestream

This repository provides a Litestream integration module for the [restinpieces](https://github.com/caasmo/restinpieces) framework. It allows you to easily add continuous backup capabilities for your application's SQLite database using Litestream.

## Configuration

Litestream configuration is managed securely through the restinpieces `SecureConfigStore`.

1.  **Generate a Blueprint:** Use the included tool to generate a template TOML configuration file:
    ```bash
    go run ./cmd/generate-blueprint-config -o litestream.blueprint.toml
    ```
    See [cmd/generate-blueprint-config/main.go](./cmd/generate-blueprint-config/main.go) for details.

2.  **Customize:** Edit the generated `litestream.blueprint.toml` file with your specific replica settings (e.g., S3 bucket, region, credentials). **Note:** The `db_path` is not set in the config file; it's provided by the main application during initialization.

3.  **Encrypt and Store:** Use the `insert-config` tool provided by the [restinpieces](https://github.com/caasmo/restinpieces) framework to encrypt the TOML file using your age key and store it in the database. Use the scope defined by `litestream.ConfigScope` (default: "litestream"). Example:
    ```bash
    # Assuming insert-config is built and in your PATH
    # and restinpieces is in your GOPATH
    go build -o bin/insert-config ../restinpieces/cmd/insert-config
    ./bin/insert-config \
      -age-key /path/to/your/age.key \
      -db /path/to/your/app.db \
      -file litestream.blueprint.toml \
      -scope litestream \
      -format toml \
      -desc "Initial Litestream configuration"
    ```

## Integration Example

Refer to [cmd/example/main.go](./cmd/example/main.go) to see how to:
*   Initialize the `restinpieces.Server`.
*   Load the Litestream configuration from the secure store.
*   Instantiate the `litestream.Litestream` service.
*   Add it as a daemon to the `restinpieces.Server`.

## CGO Driver Compatibility

**Important:** The underlying [Litestream library](https://github.com/benbjohnson/litestream) uses the [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) driver, which relies on CGO.

If your main application uses a different CGO-based SQLite driver, such as [crawshaw.io/sqlite](https://crawshaw.io/sqlite), you will encounter compilation errors due to conflicting CGO definitions.

The `restinpieces` framework provides a separate database implementation for the Crawshaw driver here: [caasmo/restinpieces-sqlite-crawshaw](https://github.com/caasmo/restinpieces-sqlite-crawshaw).

**You cannot use this `restinpieces-litestream` module if your application is built with the Crawshaw SQLite driver (or another conflicting CGO driver).** Litestream currently requires `mattn/go-sqlite3`.

## SQLite PRAGMAs for Litestream

Consider setting the following PRAGMAs in your application when initializing the database connection for optimal performance and compatibility with Litestream:

https://litestream.io/tips/

Disable autocheckpoints for high write load servers.  Prevent aplication to do
checkpoints:

    PRAGMA wal_autocheckpoint = 0;

This mode will ensure that the fsync() calls are only called when the WAL
becomes full and has to checkpoint to the main database file. This is safe as
the WAL file is append only. Should be probably default when using WAL:

    PRAGMA synchronous = NORMAL;

Litestream requires periodic but short write locks on the database when
checkpointing occurs. SQLite will return an error by default if your
application tries to obtain a write lock at the same time.
This pragma will wait up to a given number of milliseconds before failing a
query if it is blocked on a write.

    PRAGMA busy_timeout = 5000;

