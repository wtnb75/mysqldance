# mysqldance

[![main](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml/badge.svg)](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml)

A CLI that connects directly to MySQL and writes databases to a flat directory of SQL files with **one record per line and fully deterministic output**. The same data always produces the same bytes, so you can keep dumps in Git and review diffs, or hand them straight to the official MySQL Docker image's `/docker-entrypoint-initdb.d` to restore.

Targets MySQL 8.0 and later (including 8.4). MariaDB and 5.7 are not supported.

## Build

```sh
go build ./cmd/mysqldance
```

## Usage

```sh
MYSQL_PWD=secret mysqldance -h 127.0.0.1 -P 3306 -u root -o ./dump mydb otherdb
```

Put flags **before** the database names (Go's standard `flag` package stops parsing at the first positional argument).

| Flag | Default | Description |
| --- | --- | --- |
| `-h`, `--host` | `127.0.0.1` | Host to connect to |
| `-P`, `--port` | `3306` | Port to connect to |
| `-u`, `--user` | (none) | User name |
| `--socket` | (none) | Unix socket. Takes precedence over host and port |
| `--password-file` | (none) | File containing the password. One trailing newline is stripped |
| `--ssl-mode` | `preferred` | TLS mode: `disabled`, `preferred`, `required`, `verify-ca` or `verify-identity` (see "TLS" below) |
| `--ssl-ca` | (none) | PEM file of CA certificates used to verify the server. Only with `required`, `verify-ca` or `verify-identity`; required by `verify-ca` |
| `-o`, `--output` | (required) | Output directory. Created if missing |
| `--rows-per-insert` | `1000` | Average number of rows per `INSERT` statement |
| `--max-insert-bytes` | `1048576` | Upper bound on the size of one `INSERT` statement, in bytes. The statement is split right after the row that exceeds it |
| `--no-lock` | off | Do not take the global read lock when a table is not InnoDB; warn instead (see "Non-InnoDB tables and the read lock" below) |
| positional | (at least one required) | Names of the databases to dump |

- The password is never accepted on the command line. If `--password-file` is given its contents are used; otherwise the `MYSQL_PWD` environment variable is used.
- The exit code is 0 on success and 1 on any error (including usage errors). Errors and warnings go to standard error.
- Naming a system database (`information_schema`, `mysql`, `performance_schema`, `sys`) is an error.

### TLS

The dump can contain personal data, and the password exchange and every row travel over the connection, so use TLS for any server that is not on the same host. The modes are modeled on the `mysql` client's `--ssl-mode` (values are lowercase, with hyphens):

| `--ssl-mode` | Behavior |
| --- | --- |
| `disabled` | Never use TLS |
| `preferred` (default) | Use TLS if the server supports it, otherwise fall back to clear text. The certificate is not verified, so this protects against eavesdropping only |
| `required` | Use TLS; fail if the server does not support it. The certificate is not verified unless `--ssl-ca` is given, in which case its chain is checked against that CA |
| `verify-ca` | Like `required`, and always verify the certificate chain against the CAs in `--ssl-ca` (required). The host name is not checked |
| `verify-identity` | Verify the certificate chain and that the certificate matches `--host`. Uses `--ssl-ca` if given, otherwise the system CAs. Cannot be combined with `--socket` |

- Before this option existed, connections were always clear text. The default `preferred` encrypts when the server allows it; use `--ssl-mode disabled` to get the old behavior.
- Only `preferred` can fall back to clear text. A server that does not support TLS makes every stricter mode fail before credentials are sent.
- Over a Unix socket (`--socket`), `preferred` does not use TLS, as in the `mysql` client. The other modes still apply.
- `verify-identity` uses Go's host name check, which needs the server certificate to carry a Subject Alternative Name matching `--host`; a certificate with only a Common Name is rejected.
- `--ssl-ca` with `disabled` or `preferred` is a usage error, because the file would be ignored. Client certificates are not supported.

### Restoring with Docker

```sh
docker run -d -e MYSQL_ROOT_PASSWORD=root -v ./dump:/docker-entrypoint-initdb.d mysql:8.4
```

In essence this is `docker run -v ./dump:/docker-entrypoint-initdb.d mysql` (the official image refuses to start without something like `MYSQL_ROOT_PASSWORD`, hence the extra flag above). On first start the entrypoint feeds the `*.sql` files in lexicographic file name order, restoring the databases. When loading by hand with the `mysql` client, also run the files one at a time in lexicographic file name order.

## Output format

The output directory is flat (no subdirectories). For each database, these files are written:

| File | Content |
| --- | --- |
| `<db>.00-schema.sql` | The fixed header (with `CREATE DATABASE IF NOT EXISTS` between the `SET` statements and `USE`), then `DROP ... IF EXISTS` and `CREATE` for tables (by name) and views (in dependency order) |
| `<db>.10-data.<table>.sql` | The fixed header, then the table's `INSERT` statements |

Every file starts with the same fixed header. In the schema file, `CREATE DATABASE IF NOT EXISTS ...;` goes between the `SET` statements and `USE` (`SET NAMES utf8mb4` runs first so that non-ASCII database names are read correctly even when the client's default character set is not `utf8mb4`).

```sql
SET NAMES utf8mb4;
SET TIME_ZONE='+00:00';
SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';
SET FOREIGN_KEY_CHECKS=0;
SET UNIQUE_CHECKS=0;
USE `<db>`;
```

A data file has ``INSERT INTO `t` VALUES`` followed by the values, one record per line. Generated columns are not written, and column names are listed explicitly (``INSERT INTO `t` (`c1`, ...) VALUES``) only for tables that have a generated or `INVISIBLE` column. A table with zero rows produces a file containing only the header.

### File name escaping

In file names, database and table names keep only ASCII letters, digits, `_` and `-` as they are; every other byte (`.`, `%`, spaces, each byte of non-ASCII UTF-8, and so on) becomes `%XX` (uppercase hex). Examples: `my.db` → `my%2Edb`, `日本` → `%E6%97%A5%E6%9C%AC`. Because the separator `.` never appears inside a name, databases that share a prefix, such as `mydb` and `mydb2`, can live in one directory without breaking the lexicographic order of each database's schema → data files.

- **255-byte limit**: if an escaped file name exceeds 255 bytes the run fails and the output directory is left unchanged (a non-ASCII table name grows to roughly 9 bytes per character).
- **Names differing only in case**: if two output file names become identical when lowercased, such as `Users` and `users` (across all databases in one run), the run fails. This prevents files from overwriting each other on case-insensitive file systems.

## Determinism and ordering

- For the same data, every file is byte-for-byte identical across runs.
- Row order: primary key; if there is none, the first NOT NULL unique key (by index name in lexicographic order); if there is neither, the byte order of each row's output string. String keys are compared as binary, not by collation.
- `INSERT` statement boundaries are decided by row content (a hash), so updating one row normally changes just that row in the diff (one deletion plus one addition). Adding or deleting rows only changes the area around them.
- Value formats: strings are `'...'` (with `\`, `'`, `"`, newlines, NUL and so on escaped); `BLOB`, `BINARY`, `VARBINARY` and `GEOMETRY` are `0x` followed by hex (empty is `''`); `BIT` is `b'...'`; NULL is `NULL`. Times are read and written with `TIME_ZONE='+00:00'`.
- The `DEFINER=...` clause of views is not written (views are created as the restoring user).
- Reads use a single connection and a single transaction (`REPEATABLE READ` with `START TRANSACTION WITH CONSISTENT SNAPSHOT`) for all databases, so every table of every database is from the same point in time. Schema objects are read from the catalog (`information_schema`, `SHOW CREATE`), which the snapshot does not cover, so DDL run during the dump may still show up.
- The reading connection pins `sql_mode=''`, `sql_quote_show_create=1` and `show_create_table_verbosity=0` at session level, so output does not change even if the server's default `sql_mode` has `ANSI_QUOTES` (identifiers would become `"t"`, which cannot be restored) or `PAD_CHAR_TO_FULL_LENGTH` (`CHAR` values would be padded with spaces).
- A table with a column that cannot be read because only column-level privileges exist (`GRANT SELECT (c1, c2) ON ...`) is an error rather than silently dropping the column (the columns of `SHOW CREATE TABLE` are cross-checked against `information_schema.COLUMNS`).

### Non-InnoDB tables and the read lock

The snapshot transaction does not cover tables of non-transactional engines (MyISAM and so on). So when any base table of any dumped database is not InnoDB, the tool runs `FLUSH TABLES WITH READ LOCK` first, then starts the snapshot, and holds the lock until the run has finished reading and writing the files (the connection is closed). Every table is then from the same point in time. If every table is InnoDB, no lock is taken.

- **Effect on the server:** while the lock is held, writes to **all** tables of **all** databases wait (reads are not affected), and `FLUSH TABLES WITH READ LOCK` itself waits for running long statements to finish, which in turn holds up statements that arrive after it. The lock lasts for the whole read, so a large dump stalls writers for that long. Dump from a replica when that matters.
- **Required privilege:** `RELOAD`. If the account lacks it, the run fails with an error that says so; nothing is written.
- **`--no-lock`:** skips the lock. Non-InnoDB tables are then read without consistency and a warning is printed for each (see "Warnings").
- If a non-InnoDB table is ever read while no lock is held (for example because it appeared after the tool found every table to be InnoDB), the run fails instead of reading it inconsistently; run it again or pass `--no-lock`.

### AUTO_INCREMENT is not restored

`AUTO_INCREMENT=N` (the counter's current value) is removed from `CREATE TABLE` so that a counter moving by itself does not produce a diff. After restoring, numbering continues from "existing maximum + 1", so if rows at the end were deleted in the past, the next number differs from the original server. The same applies to partitioned tables.

### Warnings

With `--no-lock`, tables that are not InnoDB (MyISAM and so on) cannot guarantee snapshot consistency, so a warning is printed and the dump continues. A table whose estimated row count (`information_schema.TABLES.TABLE_ROWS`) exceeds 500,000 gets a warning, regardless of keys, that it is far beyond the intended scale and that all rows will be loaded into memory (and, if it has no key, that it will be sorted in memory).

## Re-dumping and removing stale files

Re-dumping into the same output directory replaces **each dumped database's files** (`<db>.00-schema.sql` and `<db>.10-data.*.sql`) once every database has been written, and deletes the files of that database from last time that were not written again. Data files of tables that existed last time but are gone now therefore do not remain. Files of other databases and unrelated files such as `README.txt` are left alone. New files are always created fresh under temporary names (existing paths are never followed).

A table hidden from the dump account by missing privileges is indistinguishable from a dropped table. If a table that was visible last time becomes invisible after a privilege change, its data file is deleted as stale and is not part of the new output. Always dump with an account that has the same privileges.

### Failure handling

A run has two phases. Databases are processed one at a time, in the order given, and every file is written under a temporary name (`.mysqldance-NNNN.tmp` in the output directory; not `*.sql`, so `docker-entrypoint-initdb.d` ignores it). Only when every database has been written does the run **commit**: it renames the temporary files into place (each rename replaces the old file atomically) and deletes the files from last time that were not written again.

- **Failure before the commit** (a file name over 255 bytes, a case-only collision, an entry with one of the database's file names that is not a regular file such as a symlink or directory, a read error, a lost connection, a write error, Ctrl-C (`SIGINT`), `SIGTERM` as sent by `docker stop` and `kill`, and so on): nothing at a final path changes, whichever database fails. The previous dump stays complete, and the temporary files are removed.
- **Failure during the commit** (a rename or delete error): the output directory can hold a mix of new and old files. File systems have no atomic update of several files, so this window cannot be closed; it is only as long as the renames take. Re-run the dump, or, if the output directory is under Git, restore the previous state from Git.
- **A second signal** (a second Ctrl-C or `SIGTERM` while the run is cleaning up) is not caught: it terminates the process at once, as `SIGKILL` would, so the next run sweeps any leftover temporary files.
- **Killed processes** (`SIGKILL`, power loss): the process cannot clean up. Temporary files may remain, and the next run deletes them before it starts. Only if the kill lands in the commit window is the output a mix as described above.

In all cases the exit code is 1. On exit, the snapshot transaction is rolled back with a 5-second limit; if the server does not answer in time, the connection is closed instead, which ends the transaction on the server.

Because the old files stay until the commit, the output directory needs room for the old and the new dump at the same time. Temporary files are synced to disk before the renames, so a crash does not leave empty files in place of the previous dump. Do not run two dumps into the same output directory at once.

## Security

A dump contains the table data itself, which may include personal information or credentials. As with `mysqldump > file`, files are created with permissions that follow your umask (directories are 0755), so protect the output location with the umask, directory permissions, encrypted storage and so on. Before committing to a shared Git repository, confirm the data is fine to publish. Note too that overly restrictive permissions can make the files unreadable to the `mysql` user inside the container when used as `docker-entrypoint-initdb.d`.

## Limitations (out of scope for the MVP)

- Parallel fetching, DBMSs other than MySQL (including MariaDB), and 5.7 and earlier.
- Range-split output for huge tables. All rows are read into memory before being written (the intended scale is about 10 tables per database and up to about 100,000 rows).
- Triggers, stored routines, events, and users and privileges.
- Flags must be placed before the database names.

## Development

```sh
task test                                   # unit tests (no MySQL needed; integration tests are skipped)
task test:integration                       # start MySQL 8.0 and 8.4 in Docker and run integration tests
task test:integration HOST=host.docker.internal TESTFLAGS=-v   # when the Docker daemon is on another host
task lint                                   # golangci-lint
```

Integration tests run only when the `MYSQLDANCE_TEST_DSN` environment variable is set (for example `root:root@tcp(127.0.0.1:33061)/`).
CI (`.github/workflows/integration.yml`) runs them against MySQL 8.0 and 8.4 on every push and fails the job if they are skipped.
