# restinpieces-litestream

This package allows you to use Litestream's continuous backup capabilities "in-process" with your [restinpieces](https://github.com/caasmo/restinpieces) application. It removes the need to manage a separate Litestream binary, systemd service, and standalone configuration files.

Instead, it provides a `restinpieces` daemon that is compiled into your application binary and integrates directly with the framework's lifecycle and secure configuration store.

## Quickstart Example

This section provides a step-by-step guide to get the example application running and see the backup/restore process in action. The example application itself can be found in [cmd/example/main.go](./cmd/example/main.go).

### Step 1: Configure and Store Litestream Settings

First, you need to provide a Litestream configuration file. This package uses the standard `litestream.yml` format.

1.  **Create `litestream.yml`:**
    Create a `litestream.yml` file with your desired backup replica settings. For example, to back up to a local directory:
    ```yaml
    dbs:
      - path: ./app.db
        replicas:
          - type: file
            path: ./litestream-replicas
    ```
    For more details and other replica types like S3, see the [official Litestream configuration documentation](https://litestream.io/reference/config/).

2.  **Store the Configuration:**
    The `restinpieces` framework stores all configuration securely inside the application database. Use the `ripc` command-line tool (provided by the `restinpieces` project) to encrypt and save your `litestream.yml`.
    ```bash
    # This command assumes you have an age key and the ripc tool.
    # It creates a new app.db if it doesn't exist and saves the config inside it.
    ripc -age-key age_key.txt -dbpath app.db config save -scope litestream litestream.yml
    ```
    This command saves the configuration under the `litestream` scope, which the daemon will use to load its settings on startup.

### Step 2: Build the Example Application

This repository includes an example application that demonstrates how to integrate the Litestream daemon.

Build the application using the standard Go toolchain:
```bash
go build -o myapp ./cmd/example
```
This compiles the code in `cmd/example` and creates an executable file named `myapp`.

### Step 3: Run the Application

Now, run the compiled application:
```bash
./myapp
```
This will start the main web server and, alongside it, the Litestream daemon will begin monitoring `app.db` and replicating any changes to your configured destination. You should see log output from both the application and Litestream indicating that they have started.

While the app is running, you can make changes to the database (e.g., by using `sqlite3 app.db "CREATE TABLE t(id INT);"`), and you will see Litestream backing them up.

### Step 4: Perform a Local Restore

To simulate a disaster recovery scenario, you can use the official `litestream` binary to restore the database from your replica.

1.  **Install Litestream:**
    If you don't have it, [install the Litestream binary](https://litestream.io/installation).

2.  **Restore the database:**
    Stop your running application (`Ctrl+C`). You can delete the local `app.db` and `app.db-wal` files to simulate a complete data loss. Then, run the `litestream restore` command. The replica URL must match what you defined in your `litestream.yml`.
    ```bash
    # The replica URL is the destination from your config
    litestream restore -o app.db ./litestream-replicas
    ```
    This command will fetch the latest snapshot and subsequent WAL files from your replica and restore the `app.db` file to its most recent state. You can now restart your application, and it will have all its data back.

## Configuration

This package uses the standard `litestream.yml` configuration format. Litestream configuration is managed securely through the restinpieces `SecureConfigStore`, as shown in the Quickstart guide.

For more information on `ripc`, see the [`ripc` documentation](https://github.com/caasmo/restinpieces/blob/master/doc/ripc.md).

**Note: No Environment Variables**
A key principle of the `restinpieces` framework is that all configuration must be self-contained and securely stored in the database to create a single, auditable source of truth. Therefore, this package **does not support environment variable expansion** (e.g., `$HOME` or `${VAR}`) within the `litestream.yml` file.

To enforce this, a validation check runs on startup. The presence of environment variable syntax will cause the application to fail, **even if the variables are inside comments**. Please ensure your configuration contains only explicit paths and values.

### Deactivated Upstream Features

The upstream Litestream project includes several features designed for when it is run as a standalone binary. When using `restinpieces-litestream` as an embedded Go library, some of these features are not suitable and have been deactivated. Specifically, configuration options related to the **`exec` subcommand**, the **MCP server**, and **Prometheus metrics** are not supported.

## Logging

The upstream Litestream project is designed primarily as a standalone binary. This architecture makes it difficult to cleanly inject a custom `slog.Logger` when using Litestream as an embedded library, as its internal components fall back to a global default logger.

To solve this without requiring a heavily modified and hard-to-maintain fork, this package uses a "split-logging" model. Our compromise is to expose Litestream's own logging configuration, allowing us to control its output separately from the main framework logger.

This package follows a **"split-logging"** model:

1.  **Framework Logs:** All logs generated by the `restinpieces-litestream` daemon wrapper itself are sent to the main framework's `slog.Logger` instance. If you are using the default `restinpieces` setup, these logs will be written to the SQLite database with full structured context.

2.  **Internal Litestream Logs:** All internal logs from the core Litestream components (e.g., relating to replication, snapshots, compaction) are sent to either **`os.Stdout`** (the default) or **`os.Stderr`**. This behavior mimics running Litestream as a separate binary alongside the main application.

### Configuring Internal Logs

The destination, format, and level of these internal logs can be controlled via the `litestream.yml` configuration file. This is handled by a function from our Litestream fork that configures the library's internal global logger.

Example `litestream.yml` logging section:
```yaml
logging:
  # 'debug', 'info', 'warn', or 'error'
  level: 'info' 
  # 'text' or 'json'
  type: 'text'
  # Direct logs to stderr instead of stdout. Defaults to false.
  stderr: false
```
This configuration would cause the internal Litestream logs to be written to `os.Stdout` as text at the `INFO` level, while the main framework logs continue to go to their own destination.

## Driver Compatibility

As of v0.5.0, the underlying [Litestream library](https://github.com/benbjohnson/litestream) uses the excellent [modernc.org/sqlite](https://modernc.org/sqlite) driver, which is **pure-Go**.

This is a major advantage as it means this package has **no CGO dependency**.
*   No C compiler or external dependencies are needed to build your application.
*   Cross-compilation is simple.
*   There are no conflicts with any other SQLite drivers (whether pure-Go or CGO-based) that your main application might use.

This aligns perfectly with the `restinpieces` framework, whose default database driver is [zombiezen.com/go/sqlite](https://zombiezen.com/go/sqlite), which is also a pure-Go driver. This ensures a CGO-free environment when using the default settings for both the framework and this package.

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
