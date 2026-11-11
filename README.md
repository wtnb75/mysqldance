# mysqldance

[![main](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml/badge.svg)](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml)

A CLI that connects directly to MySQL and writes databases to a flat directory of SQL files with **one record per line and fully deterministic output**. The same data always produces the same bytes, so you can keep dumps in Git and review diffs, or hand them straight to the official MySQL Docker image's `/docker-entrypoint-initdb.d` to restore.

Targets MySQL 8.0 and later (including 8.4), MariaDB (tested on 10.11 and 11.4; see [MariaDB](#mariadb)) and MySQL 5.7 (tested on 5.7.44 only; see [MySQL 5.7](#mysql-57)). MySQL 5.6 and earlier are not supported.

## Build

```sh
go build ./cmd/mysqldance
```

To build release-style binaries for every supported OS and architecture (linux, darwin and windows on amd64 and arm64) into `dist/`, run `task snapshot` (requires [GoReleaser](https://goreleaser.com)).

Pushing a tag that starts with `v` (for example `v1.0.0`) runs the `tag` workflow, which publishes a GitHub Release with the archives, `checksums.txt` and a changelog.

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
| `--target` | `mysql` | The DBMS the files are written for: `mysql`, `sqlite` or `postgres` (see "SQLite output" and "PostgreSQL output" below). Any other value is a usage error |
| `--rows-per-insert` | `1000` | Average number of rows per `INSERT` statement |
| `--max-insert-bytes` | `1048576` | Upper bound on the size of one `INSERT` statement, in bytes. The statement is split right after the row that exceeds it |
| `--rows-per-file` | `0` | Average number of rows per data file. A table with more rows is split into several files (see "Splitting large tables" below). `0` never splits |
| `--file-mode` | (umask) | Exact octal permission of the output files, e.g. `0640`. The owner must have read permission. By default files get `0666` filtered by the umask |
| `--dir-mode` | (umask) | Exact octal permission of the output directory, e.g. `0750`, applied only when mysqldance creates it (parent directories it creates get `0755` filtered by the umask). The owner must have `rwx`. An existing directory is never changed. By default `0755` filtered by the umask |
| `--no-lock` | off | Do not take the global read lock when a table is not InnoDB; warn instead (see "Non-InnoDB tables and the read lock" below) |
| `--lock-timeout` | `30s` | How long `FLUSH TABLES WITH READ LOCK` may wait for the lock, as a duration (`30s`, `2m`; rounded up to whole seconds). When it has not got the lock by then, the run fails and nothing is written. `0` sets no limit of its own: the server's `lock_wait_timeout` applies (see "Non-InnoDB tables and the read lock" below) |
| `-j`, `--jobs` | `1` | Number of tables read in parallel, each on a connection of its own, from 1 to 64. The default `1` reads everything on one connection, as before, and needs no `RELOAD` privilege. Above `1` the global read lock is always taken, so the account needs `RELOAD`, and `--no-lock` cannot be combined with it (see "Parallel reads" below) |
| `--log-level` | `info` | Lowest level of the log records written to standard error: `debug`, `info`, `warn` or `error`. Warnings are `warn` records, so `error` hides them (see "Warnings" below) |
| `--log-format` | `text` | Format of the log records: `text` (`key=value`, as `log/slog`'s text handler writes it) or `json` (one JSON object per line) |
| `--version` | off | Print the version and commit to standard output, then exit with 0. Builds made without the release ldflags print `dev` and `unknown` |
| positional | (at least one required) | Names of the databases to dump |

- The password is never accepted on the command line. If `--password-file` is given its contents are used; otherwise the `MYSQL_PWD` environment variable is used.
- The exit code is 0 on success and 1 on any error (including usage errors). Errors and warnings go to standard error.
- Naming a system database (`information_schema`, `mysql`, `performance_schema`, `sys`) or a database that does not exist is an error. Every name is checked right after connecting, before the output directory is touched and before any lock is taken, so one bad name among several stops the run with nothing created, written or deleted.

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
| `<db>.20-triggers.sql` | Only for a database that has triggers (see "Triggers"): the fixed header, then `DROP TRIGGER IF EXISTS`, `SET SQL_MODE` and `CREATE TRIGGER` for each trigger |

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

### Triggers

A database with triggers also gets `<db>.20-triggers.sql` (a database without any gets no such file, and one left from an earlier dump is removed). The name sorts after every data file, so the triggers are created once all rows are in and loading fires none of them; it sorts before `<db>.90-constraints.sql`.

- Each trigger is written as `DROP TRIGGER IF EXISTS`, `SET SQL_MODE='...'` and the statement of `SHOW CREATE TRIGGER`. The `sql_mode` is the one the trigger was created under, because it changes how the body is parsed (`ANSI_QUOTES`, for one).
- **The `DEFINER=...` clause is not written**, as for views: the triggers are created as the user who restores the dump, and the name of the dumping user stays out of the output.
- Bodies may contain `;`, so the statements end with a delimiter set by `DELIMITER` (`$$`; if a statement contains it, or ends with a character that would join it, the next of `//`, `##`, `%%`, `!!`, `$$$` and so on). A trigger with a line that starts with `DELIMITER` fails the run, because the client would run that line as a command. `DELIMITER` is a command of the `mysql` client: the file restores with the client (which is what `docker-entrypoint-initdb.d` runs), not through a driver that sends the whole file as one multi-statement query.
- Triggers are ordered by table, timing (`AFTER` before `BEFORE`) and event, then in the order the server runs them (their creation order), then by name. Triggers of the same table, timing and event are therefore created in the order they run in, so the restored ones run in that order too.
- The character set and collation of the connection the trigger was created with are not kept: the file runs with `SET NAMES utf8mb4`.
- A trigger whose statement cannot be read (the account lacks the `TRIGGER` privilege) is an error, not a silent omission.
- `--target sqlite` and `--target postgres` do not write triggers (their bodies are MySQL syntax); a warning names each one.

### SQLite output

`--target sqlite` writes the same files (same names, same one-record-per-line `INSERT`s, same splitting and determinism) in SQL that SQLite runs. The files of one MySQL database restore into one SQLite database; run them in lexicographic file name order:

```sh
LC_ALL=C cat dump/mydb.*.sql | sqlite3 mydb.db
```

- The schema file has no `CREATE DATABASE`, `USE` or `SET`: it starts with `PRAGMA foreign_keys=OFF;`, then one `CREATE TABLE` per table (by name), each followed by its `CREATE [UNIQUE] INDEX` statements. The table definitions are built from `information_schema`, not from `SHOW CREATE TABLE`. A data file runs in one transaction (`PRAGMA foreign_keys=OFF; BEGIN; ... COMMIT;`).
- Column types keep every value exact. Integer types, `YEAR` and `BIT(n)` with `n` up to 63 become `INTEGER`; `FLOAT` and `DOUBLE` become `REAL`; binary and spatial types become `BLOB`. `DECIMAL`, `BIGINT UNSIGNED` and `BIT(64)` become `TEXT`, because SQLite would store a number above 2^63-1 or a decimal as a lossy `REAL`; their values are written as strings. All other types (strings, `ENUM`, `SET`, `JSON`, dates and times) become `TEXT`. `NOT NULL`, the primary key, foreign keys (with `ON DELETE` / `ON UPDATE`), `UNIQUE` keys and ordinary indexes (with `DESC`) are carried over, and so are literal defaults and `DEFAULT CURRENT_TIMESTAMP`.
- Index names are made unique in the database as `<table>__<index>`; if that collides with another table or index name, the run fails.
- A line break inside a string value is written as `'a'||char(10)||'b'`, so one record stays on one line.
- The files need `sqlite3` 3.7.16 or newer: they use multi-row `VALUES` (3.7.11) and `char()` (3.7.16), according to SQLite's release notes. The restore is verified with `sqlite3` 3.46.1 and 3.8.2 only; 3.7.9 rejects the `INSERT`s, and 3.7.16 to 3.8.1 were not run.
- MySQL's `AUTO_INCREMENT` is not carried over (as in the MySQL output, the counter is not restored). Table and column comments, character sets and collations are dropped; text compares as plain bytes in SQLite, which is never stricter than MySQL's collations, so the data always loads.
- **Not output, with a warning for each** (a `WARN` record on standard error with the attribute `database=db`, see "Warnings"; the dump continues): views (their `SELECT` is MySQL syntax; converting them is a separate backlog item), generated columns (their values are not dumped either), `ON UPDATE CURRENT_TIMESTAMP`, default expressions other than `CURRENT_TIMESTAMP`, `FULLTEXT` and `SPATIAL` indexes, indexes with an expression key part or on a generated column, CHECK constraints, and foreign keys into another database. A prefix index (`col(10)`) is created on the whole column, which is also reported. Triggers are not output either, with a warning for each (their bodies are MySQL syntax). Routines and events are not dumped for any target.
- A column type with no SQLite counterpart makes the run fail instead of guessing.

### PostgreSQL output

`--target postgres` writes the same one-record-per-line `INSERT`s, with the same splitting and determinism, in SQL that PostgreSQL runs. One MySQL database becomes one **schema** of the same name, so several databases fit into one PostgreSQL database. For each database, three kinds of files are written:

| File | Content |
| --- | --- |
| `<db>.00-schema.sql` | `CREATE SCHEMA IF NOT EXISTS`, then one `CREATE TABLE` per table (by name), each followed by its `CREATE [UNIQUE] INDEX` statements |
| `<db>.10-data.<table>.sql` | The table's `INSERT` statements, in one transaction |
| `<db>.90-constraints.sql` | The foreign keys (`ALTER TABLE ... ADD CONSTRAINT`), always written, with no statement when there is none |

The foreign keys are in the last file because the data files run in table-name order, which no foreign key respects. Every file sets what it needs itself (`client_encoding`, `standard_conforming_strings`, `TimeZone = 'UTC'`, `search_path`), because the official image runs each file in a session of its own, so they restore with the official image's `/docker-entrypoint-initdb.d` (or `psql -f` on each file in file name order) into an empty database. Restoring into a schema that already has the tables fails.

- **Types** keep every value exact. `TINYINT(1)` (`BOOLEAN`) becomes `boolean`; every other `TINYINT`, `SMALLINT`, `MEDIUMINT`, `INT`, `BIGINT` and `YEAR` becomes the narrowest PostgreSQL integer type that holds the whole range (`TINYINT UNSIGNED` is `smallint`, `INT UNSIGNED` is `bigint`, `BIGINT UNSIGNED` is `numeric(20,0)`). `DECIMAL(p,s)` is `numeric(p,s)`, `FLOAT` is `real`, `DOUBLE` is `double precision`, `BIT(n)` is `bit(n)`, `CHAR(n)` and `VARCHAR(n)` are `varchar(n)` (PostgreSQL's `char(n)` pads, MySQL's does not), the text types, `ENUM` and `SET` are `text`, `JSON` is `json`, binary and spatial types are `bytea`, `DATE` is `date`, `DATETIME(n)` is `timestamp(n)`, `TIMESTAMP(n)` is `timestamptz(n)` and `TIME(n)` is `interval(n)` (its range, -838:59:59 to 838:59:59, is wider than a time of day).
  - Only a plain `TINYINT(1)` is `boolean`, which is what MySQL 8.0.19 and later reports as `tinyint(1)`; `TINYINT(1) UNSIGNED` is reported as `tinyint unsigned` and becomes `smallint`. A `TINYINT(1)` row holding anything but 0 or 1 makes the run fail.
- `NOT NULL`, the primary key, `UNIQUE` keys, ordinary indexes (with `DESC`) and literal defaults (and `DEFAULT CURRENT_TIMESTAMP`) are carried over. Primary keys and indexes are named `<table>__<index>` (the primary key is `<table>__PRIMARY`); if that collides with another table or index name, the run fails.
- A line break inside a string value is written as an `E'a\nb'` string, so one record stays on one line.
- MySQL's `AUTO_INCREMENT` is not carried over: the column is a plain integer, the counter is not restored (as in the MySQL output), and a later `INSERT` has to give the key. Comments, character sets and collations are dropped.
- **A value or name PostgreSQL cannot hold fails the run** with the table and column named: a NUL byte in text, a zero or partial date (`0000-00-00`, `2024-00-10`), a name over 63 bytes, a database name starting with `pg_`, a column type with no PostgreSQL counterpart.
- **Not output, with a warning for each** (a `WARN` record on standard error with the attribute `database=db`, see "Warnings"; the dump continues): views (their `SELECT` is MySQL syntax; converting them is a separate backlog item), generated columns (their values are not dumped either), `ON UPDATE CURRENT_TIMESTAMP`, default expressions other than `CURRENT_TIMESTAMP`, `FULLTEXT` and `SPATIAL` indexes, indexes with an expression key part or on a generated column, CHECK constraints, and foreign keys that PostgreSQL cannot take: into another database (the constraints file of one database runs before the schema file of a later one), onto columns that are not a primary or unique key (InnoDB accepts any index, PostgreSQL does not), or onto a generated column. A prefix index (`col(10)`) is created on the whole column, which is also reported. Triggers are not output either, with a warning for each (their bodies are MySQL syntax). Routines and events are not dumped for any target.

### MariaDB

A server that reports itself as MariaDB is read as MariaDB. It is not rejected by version: 10.11 and 11.4 are the versions it was tested on, and other versions are untested.

- **The output is MariaDB's own.** `CREATE TABLE` and `CREATE VIEW` are written as MariaDB's `SHOW CREATE` prints them (for example `current_timestamp()`, `int(11)`, `JSON` as `longtext` with a `CHECK`, and `CREATE DATABASE` with MariaDB's default collation). Restore the default `--target mysql` output of a MariaDB server into MariaDB. **It is not meant for MySQL**: for example, 11.4's default collation `utf8mb4_uca1400_ai_ci` does not exist in MySQL, and MySQL's `utf8mb4_0900_ai_ci` does not exist in MariaDB, so the statement fails on the other server. No conversion is made between the two.
- **Views** are ordered by their dependencies, which are read from `information_schema.VIEWS` (MariaDB has no `VIEW_TABLE_USAGE`). The account needs the `SHOW VIEW` privilege (it already does, for `SHOW CREATE VIEW`).
- **Sequences and system-versioned tables stop the run.** A `SEQUENCE` and a table `WITH SYSTEM VERSIONING` (`information_schema.TABLES.TABLE_TYPE` `SEQUENCE` / `SYSTEM VERSIONED`) are not output, and a plain `SELECT` would lose the history rows of the latter, so the run fails before any file is changed (see [Failure handling](#failure-handling)) and names the table. Drop it, or dump from a database without it.
- **Defaults** are read as MariaDB reports them, so `--target sqlite` and `--target postgres` get the same `DEFAULT` clauses as from MySQL (`current_timestamp()` becomes `CURRENT_TIMESTAMP`; other expression defaults are left out with a warning).

### MySQL 5.7

A server whose version starts with `5.7.` is read as MySQL 5.7. **Only 5.7.44 was tested** (the session settings were also tried on 5.7.21); other 5.7 releases are untested, and 5.6 and earlier are refused.

- **The output is 5.7's own.** `CREATE TABLE` is written as 5.7's `SHOW CREATE` prints it: integers keep their display width (`int(11)`, `bigint(20) unsigned`), and a database or table made with the stock `latin1` default says `DEFAULT CHARSET=latin1`. No conversion is made. Rows are read as `utf8mb4`, so the data of a `latin1` column is exact. The default `--target mysql` output restores into 5.7, and into MySQL 8.4 for the schemas tested; a zero-date default such as `DEFAULT '0000-00-00 00:00:00'` (a second `TIMESTAMP NOT NULL` column when `explicit_defaults_for_timestamp` is `OFF`, the 5.7 default) restores into 8.4 as well (8.0 was not tried), although 8.4's own `sql_mode` refuses it with `Invalid default value`: the `SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO'` in every file's header replaces the session's mode, which drops `NO_ZERO_DATE` and strict mode. A restore that skips the header (pasting only the `CREATE TABLE`) fails on 8.x.
- **`show_create_table_verbosity` is not set.** 5.7.21 and earlier do not have it, and the version is not told apart. On 5.7.22 and later a server with the variable set to `ON` globally adds the default `ROW_FORMAT` to `SHOW CREATE TABLE`, so the output follows that setting (this is untested).
- **Views** are ordered by their dependencies, which are read from `information_schema.VIEWS` (5.7 has no `VIEW_TABLE_USAGE`), as for MariaDB. The account needs the `SHOW VIEW` privilege.
- **Defaults** `CURRENT_TIMESTAMP` and `CURRENT_TIMESTAMP(n)` of a `TIMESTAMP` or `DATETIME` column are expressions, so `--target sqlite` and `--target postgres` get `DEFAULT CURRENT_TIMESTAMP` as from 8.x. The same text in a string column stays a string.
- **Foreign keys** without `ON DELETE` / `ON UPDATE` are reported by 5.7 as `RESTRICT`, and by 8.x as `NO ACTION`. `--target sqlite` and `--target postgres` write the word the server reports, so the same schema gives `RESTRICT` from 5.7 and `NO ACTION` from 8.x. Both load in SQLite and PostgreSQL, and they differ only in when PostgreSQL checks them (not with a deferred constraint).
- 5.7 has no `INVISIBLE` columns, expression keys or `CHECK` constraints (a `CHECK` is parsed and dropped by the server), and it ignores `DESC` in an index, so none of them is in the output.
- **TLS:** the official 5.7 image offers only RSA key exchange cipher suites, which Go 1.22 and later do not enable by default, so the default `--ssl-mode preferred` fails with `tls: handshake failure` against it (it does not fall back to clear text). Use `--ssl-mode disabled`, or run `mysqldance` with `GODEBUG=tlsrsakex=1` (the integration tests do). The 5.7.21 image offers TLS 1.1 at most, below the TLS 1.2 minimum, so it needs `--ssl-mode disabled`.

### Splitting large tables

With `--rows-per-file N`, the rows of a table (already in the order described under "Determinism and ordering") are cut into several data files, so that one huge file does not have to be reviewed or stored as a whole. By default (`0`) nothing is split.

| File | Content |
| --- | --- |
| `<db>.10-data.<table>.sql` | The table in one file: no split requested, or the cut produced only one file |
| `<db>.10-data.<table>.0001.sql`, `.0002.sql`, ... | The table in two or more files, in row order |

- Each file has the same fixed header, so it also runs on its own, and ends with a closed `INSERT`. Loading the files in lexicographic file name order restores the table, as before.
- The part number has at least four digits. With 10,000 parts or more, every part of that table uses the wider width, so the lexicographic order stays the numeric order.
- A file ends after a row whose primary key hash (FNV-1a, mixed) modulo `N` is zero, so on average a file holds `N` rows (the sizes vary; there is no upper bound, because a cap would make the cuts depend on position). A table without a primary key uses the columns it is ordered by instead, and a table with no usable key hashes the whole row. Updating a non-key column therefore never moves a boundary, and the diff is that one row. Adding or deleting a row moves only the boundary next to it (changing the key counts as a delete plus an add).
- Growth changes names: a table that grows from one file to several is written as `<table>.0001.sql`, `.0002.sql`, ..., and `<table>.sql` is removed as a stale file. The part numbers of the later parts shift when a boundary appears or disappears, so a Git diff may show them as renames.

### File name escaping

In file names, database and table names keep only ASCII letters, digits, `_` and `-` as they are; every other byte (`.`, `%`, spaces, each byte of non-ASCII UTF-8, and so on) becomes `%XX` (uppercase hex). Examples: `my.db` → `my%2Edb`, `日本` → `%E6%97%A5%E6%9C%AC`. Because the separator `.` never appears inside a name, databases that share a prefix, such as `mydb` and `mydb2`, can live in one directory without breaking the lexicographic order of each database's schema → data files.

- **255-byte limit**: if an escaped file name exceeds 255 bytes the run fails and the output directory is left unchanged (a non-ASCII table name grows to roughly 9 bytes per character).
- **Names differing only in case**: if two output file names become identical when lowercased, such as `Users` and `users` (across all databases in one run), the run fails. This prevents files from overwriting each other on case-insensitive file systems.

## Determinism and ordering

- For the same data, every file is byte-for-byte identical across runs.
- Row order: primary key; if there is none, the first NOT NULL unique key (by index name in lexicographic order); if there is neither, the byte order of each row's output string. String keys are compared as binary, not by collation.
- `INSERT` statement boundaries are decided by row content (a hash), so updating one row normally changes just that row in the diff (one deletion plus one addition). Adding or deleting rows only changes the area around them.
- Value formats: strings are `'...'` (with `\`, `'`, `"`, newlines, NUL and so on escaped); `BLOB`, `BINARY`, `VARBINARY` and `GEOMETRY` are `0x` followed by hex (empty is `''`); `BIT` is `b'...'`; NULL is `NULL`. Times are read and written with `TIME_ZONE='+00:00'`.
- `FLOAT` and `DOUBLE` come back with the same bits after a restore, `-0` included. A `DOUBLE` is written as the server prints it, except that `-0` is written `-0e0` (a bare `-0` is the integer 0 negated, which loses the sign). The server prints a `FLOAT` with only 6 significant digits, which loses bits, so a `FLOAT` is read as its exact `DOUBLE` value (`col * 1e0`) and written in the shortest notation that gives the same single-precision value: `0.1` stays `0.1`, and a value that needs more digits gets them (`0.33333334`, where an earlier version wrote `0.333333`). Large and small values use an exponent (`16777216` is `1.6777216e+07`; an earlier version wrote the server's `16777200`). The first dump after the upgrade therefore differs from an older one in the cells of such `FLOAT` values and `-0`; later dumps are stable. In a table with no key, rows are ordered by their output text, so rows with such a `FLOAT` may also move once.
- The `DEFINER=...` clause of views and triggers is not written (they are created as the restoring user).
- Reads use a single connection and a single transaction (`REPEATABLE READ` with `START TRANSACTION WITH CONSISTENT SNAPSHOT`) for all databases, so every table of every database is from the same point in time. With `--jobs` above 1 there are several such connections, started at one point in time (see "Parallel reads"). Schema objects are read from the catalog (`information_schema`, `SHOW CREATE`), which the snapshot does not cover, so DDL run during the dump may still show up.
- The reading connection pins `sql_mode=''`, `sql_quote_show_create=1` and `show_create_table_verbosity=0` at session level, so output does not change even if the server's default `sql_mode` has `ANSI_QUOTES` (identifiers would become `"t"`, which cannot be restored) or `PAD_CHAR_TO_FULL_LENGTH` (`CHAR` values would be padded with spaces).
- A table with a column that cannot be read because only column-level privileges exist (`GRANT SELECT (c1, c2) ON ...`) is an error rather than silently dropping the column (the columns of `SHOW CREATE TABLE` are cross-checked against `information_schema.COLUMNS`).

### Non-InnoDB tables and the read lock

The snapshot transaction does not cover tables of non-transactional engines (MyISAM and so on). So when any base table of any dumped database is not InnoDB, the tool runs `FLUSH TABLES WITH READ LOCK` first, then starts the snapshot, and holds the lock until the run has finished reading and writing the files (the connection is closed). Every table is then from the same point in time. If every table is InnoDB, no lock is taken.

- **Effect on the server:** while the lock is held, writes to **all** tables of **all** databases wait (reads are not affected), and `FLUSH TABLES WITH READ LOCK` itself waits for running long statements to finish, which in turn holds up statements that arrive after it. The lock lasts for the whole read, so a large dump stalls writers for that long. Dump from a replica when that matters.
- **Required privilege:** `RELOAD`. If the account lacks it, the run fails with an error that says so; nothing is written.
- **`--lock-timeout`:** `FLUSH TABLES WITH READ LOCK` is granted only once the statements running at that moment have finished, and statements that arrive meanwhile queue behind it. To keep one long query from stalling the whole server for an unbounded time, the wait is limited to `--lock-timeout` (default `30s`). When it runs out, the lock request is withdrawn (the queued statements go on at once), the run fails with MySQL error 1205 and an error that names `--lock-timeout`, and nothing is written. Then wait for the long statement, raise `--lock-timeout`, or pass `--no-lock`. The tool does not retry. The limit covers only the wait for the lock, not the time the lock is held afterwards. It is applied by setting `lock_wait_timeout` on the locking connection for that one statement, in whole seconds (a fraction is rounded up). `--lock-timeout 0` leaves the server's `lock_wait_timeout` in force, which is one year by default.
- **`--no-lock`:** skips the lock. Non-InnoDB tables are then read without consistency and a warning is printed for each (see "Warnings").
- If a non-InnoDB table is ever read while no lock is held (for example because it appeared after the tool found every table to be InnoDB), the run fails instead of reading it inconsistently; run it again or pass `--no-lock`.

### Parallel reads

With `--jobs N` (N above 1) the rows of N tables are read, rendered and written at the same time, each table on its own connection. The output is the same as with `--jobs 1`, byte for byte; only the time it takes changes. The schema, the table list and the warnings are still read on the first connection, one table after another, so their order does not change either.

All connections must show the same point in time, but each starts its own snapshot. So the tool uses one more connection (N + 1 in all) that holds the global read lock while the snapshots start:

1. `FLUSH TABLES WITH READ LOCK` on the extra connection. Writers wait from here on.
2. Every reading connection runs `START TRANSACTION WITH CONSISTENT SNAPSHOT` while the lock is held, so they all see the same data.
3. If every table is InnoDB, `UNLOCK TABLES` follows at once: the snapshots cover the tables, so they stay consistent. If any table is not InnoDB, the lock is kept until the run is finished, as in "Non-InnoDB tables and the read lock".

- **Required privilege:** `RELOAD`, even when every table is InnoDB (the lock is what aligns the snapshots). Without it the run fails with an error that names `RELOAD` and `--jobs 1`; nothing is written. `--jobs 1` (the default) never needs it for InnoDB-only databases.
- **No `--no-lock`:** `--jobs` above 1 with `--no-lock` is a usage error. Without the lock the snapshots would start one after another and show different points in time, and the tool neither reads such a dump silently nor falls back to one connection silently.
- **Effect on the server:** writers wait from the moment `FLUSH TABLES WITH READ LOCK` is granted until the last snapshot has started, which takes about as long as N connections need to start (tens of milliseconds, in a measurement with 4 connections). `FLUSH TABLES WITH READ LOCK` itself waits for running long statements first, and statements that arrive behind it wait too, for at most `--lock-timeout` (the run fails after that; there the error names `--jobs 1` instead of `--no-lock`, which cannot be used here). For InnoDB-only databases the lock does not last for the read; with a non-InnoDB table it does.
- **Memory:** a table with a primary key or a NOT NULL unique key is streamed (see "Limitations"), so N tables in parallel need no more memory than one. A table without a key is held in memory while it is sorted, so up to N such tables are in memory at once.
- **Server connections:** N + 1, plus the ones the server needs for itself; `max_connections` must allow them. The upper bound of `--jobs` is 64.
- DDL is not covered, as with one connection: after the lock is released, an `ALTER` or `DROP TABLE` can run while the rows are read.

### AUTO_INCREMENT is not restored

`AUTO_INCREMENT=N` (the counter's current value) is removed from `CREATE TABLE` so that a counter moving by itself does not produce a diff. After restoring, numbering continues from "existing maximum + 1", so if rows at the end were deleted in the past, the next number differs from the original server. The same applies to partitioned tables.

### Warnings

Every warning is a `WARN` record of the log (see `--log-level` and `--log-format`) on standard error; nothing is written to standard output. The record carries a `database` attribute, and a warning about one trigger also `table` and `trigger`. For example, in the text format: `time=... level=WARN msg="table `m` uses the MyISAM engine; ..." database=shop`. The time of a record is the only thing in the log that differs between runs.

With `--no-lock`, tables that are not InnoDB (MyISAM and so on) cannot guarantee snapshot consistency, so a warning is printed and the dump continues. A table without a primary key or NOT NULL unique key whose estimated row count (`information_schema.TABLES.TABLE_ROWS`) exceeds 500,000 gets a warning that it is far beyond the intended scale and that all its rows will be held in memory and sorted there. A table with a key gets none: its rows are streamed.

## Re-dumping and removing stale files

Re-dumping into the same output directory replaces **each dumped database's files** (`<db>.00-schema.sql`, `<db>.10-data.*.sql`, `<db>.20-triggers.sql` and, for `--target postgres`, `<db>.90-constraints.sql`) once every database has been written, and deletes the files of that database from last time that were not written again. Data files of tables that existed last time but are gone now therefore do not remain. Files of other databases and unrelated files such as `README.txt` are left alone. New files are always created fresh under temporary names (existing paths are never followed).

A table hidden from the dump account by missing privileges is indistinguishable from a dropped table. If a table that was visible last time becomes invisible after a privilege change, its data file is deleted as stale and is not part of the new output. Always dump with an account that has the same privileges.

### Failure handling

A run has two phases. Databases are processed one at a time, in the order given, and every file is written under a temporary name (`.mysqldance-NNNN.tmp` in the output directory; not `*.sql`, so `docker-entrypoint-initdb.d` ignores it). Only when every database has been written does the run **commit**: it renames the temporary files into place (each rename replaces the old file atomically) and deletes the files from last time that were not written again.

- **Failure before anything is read** (a bad connection or TLS setting, a database that does not exist, a system database; any one of several databases): the output directory is not touched at all. It is not created if missing, and leftover temporary files from an earlier killed run are not swept.
- **Failure before the commit** (a file name over 255 bytes, a case-only collision, an entry with one of the database's file names that is not a regular file such as a symlink or directory, a read error, a lost connection, a write error, Ctrl-C (`SIGINT`), `SIGTERM` as sent by `docker stop` and `kill`, and so on): nothing at a final path changes, whichever database fails. The previous dump stays complete, and the temporary files are removed.
- **Failure during the commit** (a rename or delete error): the output directory can hold a mix of new and old files. File systems have no atomic update of several files, so this window cannot be closed; it is only as long as the renames take. Re-run the dump, or, if the output directory is under Git, restore the previous state from Git.
- **A second signal** (a second Ctrl-C or `SIGTERM` while the run is cleaning up) is not caught: it terminates the process at once, as `SIGKILL` would, so the next run sweeps any leftover temporary files.
- **Killed processes** (`SIGKILL`, power loss): the process cannot clean up. Temporary files may remain, and the next run deletes them before it starts. Only if the kill lands in the commit window is the output a mix as described above.

In all cases the exit code is 1. On exit, the snapshot transaction is rolled back with a 5-second limit; if the server does not answer in time, the connection is closed instead, which ends the transaction on the server.

With `--jobs` above 1 this holds for every connection: on any failure and on Ctrl-C or `SIGTERM`, all reading connections and the one holding the lock are closed, so no transaction and no global read lock is left on the server. The running tables are told to stop, and no new table is started after the first failure. When several tables fail, the error reported is that of one of the failing tables (which one can depend on timing, since the first failure stops the others), never the cancel that stopped them.

Because the old files stay until the commit, the output directory needs room for the old and the new dump at the same time. Temporary files are synced to disk before the renames, so a crash does not leave empty files in place of the previous dump. Do not run two dumps into the same output directory at once.

## Security

A dump contains the table data itself, which may include personal information or credentials. As with `mysqldump > file`, files are created with permissions that follow your umask (directories are 0755), so protect the output location with the umask, directory permissions, encrypted storage and so on. `--file-mode` and `--dir-mode` set the permissions explicitly (for example `--file-mode 0600 --dir-mode 0700`); the defaults are unchanged so that the output stays readable for `docker-entrypoint-initdb.d`. Before committing to a shared Git repository, confirm the data is fine to publish. Note too that overly restrictive permissions can make the files unreadable to the `mysql` user inside the container when used as `docker-entrypoint-initdb.d`.

## Limitations (out of scope for the MVP)

- Source servers other than MySQL 5.7 and later and MariaDB (MySQL 5.6 and earlier), and output for DBMSs other than MySQL, SQLite and PostgreSQL. Parallel reading is per table (`--jobs`); one table is never read on several connections.
- Streaming for tables without a key. A table with a primary key or a NOT NULL unique key is read and written row by row, so its memory use does not grow with its size (a table of one million rows needs a few megabytes). A table without such a key is still read into memory and sorted there by the byte order of each row's output string (the intended scale for those is up to about 100,000 rows); with `--jobs N`, up to N of them are in memory at once. A temporary-file sort for them is planned.
- Stored routines, events, and users and privileges. (Triggers are dumped for `--target mysql` only.)
- Flags must be placed before the database names.

## Development

```sh
task test                                   # unit tests (no MySQL needed; integration tests are skipped)
task test:integration                       # start MySQL 8.0 and 8.4 in Docker and run integration tests
task test:integration:mariadb               # the same against MariaDB 10.11 and 11.4 (not part of the line above)
task test:integration:mysql57               # the same against MySQL 5.7.44 (amd64 image, so slow to start on arm64; not part of the line above)
task test:integration HOST=host.docker.internal TESTFLAGS=-v   # when the Docker daemon is on another host
task lint                                   # golangci-lint
```

Integration tests run only when the `MYSQLDANCE_TEST_DSN` environment variable is set (for example `root:root@tcp(127.0.0.1:33061)/`).
CI (`.github/workflows/integration.yml`) runs them against MySQL 8.0 and 8.4 on every push and fails the job if they are skipped.

### Sample data

To try `mysqldance` or look at diffs of its output, load a small sample database into a MySQL container:

```sh
task sample:up                  # start MySQL 8.4 on port 33071 and load scripts/sample/*.sql (VERSION=8.0 for 8.0; PORT=, NAME= also work)
MYSQL_PWD=root go run ./cmd/mysqldance -h 127.0.0.1 -P 33071 -u root -o ./dump sample_main sample_second サンプル
task sample:down                # remove the container
task sample:verify              # create the sample twice, dump both, and fail unless every file is byte for byte the same
```

The sample is plain SQL (`scripts/sample/`), with fixed values only (no `NOW()` or `RAND()`), so it is the same every time. It has three databases: `sample_main`, `sample_second` and `サンプル` (a non-ASCII name). `sample_main` holds strings with newlines, quotes, backslashes, NUL, Japanese and emoji; `BLOB`, `VARBINARY`, `BINARY` and `BIT`; a table without a key (with duplicate rows); a generated column and an `INVISIBLE` column; foreign keys, including a cycle; assorted date, decimal, `ENUM`, `SET` and `JSON` values; and two levels of views.

#### Big tables for measuring performance

```sh
task sample:big ROWS=1000000                   # MySQL 8.4 on port 33072, database sample_big
MYSQL_PWD=root go run ./cmd/mysqldance -h 127.0.0.1 -P 33072 -u root -o ./dump sample_big
task sample:down NAME=mysqldance-sample-big    # remove the container
```

The rows are generated on the server from the row number alone (no `NOW()` or `RAND()`), so the same variables always give the same data. `sample_big` has six tables, one per key type and row size: `int_*` (`BIGINT` primary key), `str_*` (`CHAR(32)` primary key, the MD5 of the row number, so not in insertion order) and `nokey_*` (no key), each as `*_short` (three small columns) and `*_large` (a short note and a `MEDIUMBLOB`). Variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ROWS` | `100000` | Rows per table |
| `ROWSIZE` | `both` | `short`, `large` or `both` |
| `BLOB_BYTES` | `1024` | Size of the `BLOB` column of the `*_large` tables |
| `VERSION`, `PORT`, `NAME` | `8.4`, `33072`, `mysqldance-sample-big` | MySQL image tag, host port and container name |

Rough figures for `ROWS=1000000` on MySQL 8.4 in Docker (one machine; yours will differ):

| `ROWSIZE` | Time to create | Size in MySQL |
| --- | --- | --- |
| `short` | about 20 seconds | about 0.2 GB (3 tables) |
| `both` (`BLOB_BYTES=1024`) | about 4.5 minutes | about 4.2 GB (6 tables) |

A dump of the `both` case is larger than the data in MySQL, because `BLOB` values are written as hex (twice their size); expect roughly 6 GB (an estimate, not measured). Keep about 12 GB free for `ROWS=1000000` with both row sizes, or use `ROWSIZE=short`.
