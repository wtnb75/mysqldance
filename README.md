# mysqldance

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
| `-o`, `--output` | (required) | Output directory. Created if missing |
| `--rows-per-insert` | `1000` | Average number of rows per `INSERT` statement |
| `--max-insert-bytes` | `1048576` | Upper bound on the size of one `INSERT` statement, in bytes. The statement is split right after the row that exceeds it |
| positional | (at least one required) | Names of the databases to dump |

- The password is never accepted on the command line. If `--password-file` is given its contents are used; otherwise the `MYSQL_PWD` environment variable is used.
- The exit code is 0 on success and 1 on any error (including usage errors). Errors and warnings go to standard error.
- Naming a system database (`information_schema`, `mysql`, `performance_schema`, `sys`) is an error.

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

- **255-byte limit**: if an escaped file name exceeds 255 bytes the run fails and nothing is written for that database (a non-ASCII table name grows to roughly 9 bytes per character).
- **Names differing only in case**: if two output file names become identical when lowercased, such as `Users` and `users` (across all databases in one run), the run fails. This prevents files from overwriting each other on case-insensitive file systems.

## Determinism and ordering

- For the same data, every file is byte-for-byte identical across runs.
- Row order: primary key; if there is none, the first NOT NULL unique key (by index name in lexicographic order); if there is neither, the byte order of each row's output string. String keys are compared as binary, not by collation.
- `INSERT` statement boundaries are decided by row content (a hash), so updating one row normally changes just that row in the diff (one deletion plus one addition). Adding or deleting rows only changes the area around them.
- Value formats: strings are `'...'` (with `\`, `'`, `"`, newlines, NUL and so on escaped); `BLOB`, `BINARY`, `VARBINARY` and `GEOMETRY` are `0x` followed by hex (empty is `''`); `BIT` is `b'...'`; NULL is `NULL`. Times are read and written with `TIME_ZONE='+00:00'`.
- The `DEFINER=...` clause of views is not written (views are created as the restoring user).
- Reads use one connection per database with `REPEATABLE READ` and `START TRANSACTION WITH CONSISTENT SNAPSHOT` (see "Limitations" for consistency across databases).
- The reading connection pins `sql_mode=''`, `sql_quote_show_create=1` and `show_create_table_verbosity=0` at session level, so output does not change even if the server's default `sql_mode` has `ANSI_QUOTES` (identifiers would become `"t"`, which cannot be restored) or `PAD_CHAR_TO_FULL_LENGTH` (`CHAR` values would be padded with spaces).
- A table with a column that cannot be read because only column-level privileges exist (`GRANT SELECT (c1, c2) ON ...`) is an error rather than silently dropping the column (the columns of `SHOW CREATE TABLE` are cross-checked against `information_schema.COLUMNS`).

### AUTO_INCREMENT is not restored

`AUTO_INCREMENT=N` (the counter's current value) is removed from `CREATE TABLE` so that a counter moving by itself does not produce a diff. After restoring, numbering continues from "existing maximum + 1", so if rows at the end were deleted in the past, the next number differs from the original server. The same applies to partitioned tables.

### Warnings

Tables that are not InnoDB (MyISAM and so on) cannot guarantee snapshot consistency, so a warning is printed and the dump continues. A table whose estimated row count (`information_schema.TABLES.TABLE_ROWS`) exceeds 500,000 gets a warning, regardless of keys, that it is far beyond the intended scale and that all rows will be loaded into memory (and, if it has no key, that it will be sorted in memory).

## Re-dumping and removing stale files

Re-dumping into the same output directory deletes **that database's files** (`<db>.00-schema.sql` and `<db>.10-data.*.sql`) before writing each database again. Data files of tables that existed last time but are gone now therefore do not remain. Files of other databases and unrelated files such as `README.txt` are left alone. Files are always created new (existing paths are never followed).

A table hidden from the dump account by missing privileges is indistinguishable from a dropped table. If a table that was visible last time becomes invisible after a privilege change, its data file is deleted as stale and is not part of the new output. Always dump with an account that has the same privileges.

### Failure handling

Databases are processed one at a time, in the order given. For each database the order is "check → delete → write".

- **Failure in the check phase** (a file name over 255 bytes, a case-only collision, an entry with one of the database's file names that is not a regular file such as a symlink or directory, and so on): nothing is deleted or created for the failing database.
- **Failure after deletion has started** (a read error, a lost connection, a write error, Ctrl-C and so on): that database's output is **incomplete**. Old files are already gone and some tables may have no data file. A file that fails mid-write is removed, so no truncated file is left behind. However, even with data files missing, `docker-entrypoint-initdb.d` restores the database without any error, with some tables empty. **Do not use it as is**: re-run the dump, or, if the output directory is under Git, restore the previous state from Git.
- **Multiple databases**: the databases processed earlier have already been rewritten.

In both cases the exit code is 1.

## Security

A dump contains the table data itself, which may include personal information or credentials. As with `mysqldump > file`, files are created with permissions that follow your umask (directories are 0755), so protect the output location with the umask, directory permissions, encrypted storage and so on. Before committing to a shared Git repository, confirm the data is fine to publish. Note too that overly restrictive permissions can make the files unreadable to the `mysql` user inside the container when used as `docker-entrypoint-initdb.d`.

## Limitations (out of scope for the MVP)

- Parallel fetching, DBMSs other than MySQL (including MariaDB), and 5.7 and earlier.
- Range-split output for huge tables. All rows are read into memory before being written (the intended scale is about 10 tables per database and up to about 100,000 rows).
- Triggers, stored routines, events, and users and privileges.
- Consistency across databases: when several databases are given, each is read over its own connection with its own consistent snapshot. The contents of each database are from a single point in time, but different databases can be from different times, so integrity of foreign keys or views that span databases is not guaranteed.
- Flags must be placed before the database names.

## Development

```sh
task test                                   # unit tests (no MySQL needed; integration tests are skipped)
task test:integration                       # start MySQL 8.0 and 8.4 in Docker and run integration tests
task test:integration HOST=host.docker.internal TESTFLAGS=-v   # when the Docker daemon is on another host
task lint                                   # golangci-lint
```

Integration tests run only when the `MYSQLDANCE_TEST_DSN` environment variable is set (for example `root:root@tcp(127.0.0.1:33061)/`).
