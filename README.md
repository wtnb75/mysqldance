# mysqldance

[![main](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml/badge.svg)](https://github.com/wtnb75/mysqldance/actions/workflows/main.yml)

A CLI that connects directly to MySQL and writes databases to a flat directory of SQL files with **one record per line and fully deterministic output**. The same data always produces the same bytes, so you can keep dumps in Git and review diffs, or hand them straight to the official MySQL Docker image's `/docker-entrypoint-initdb.d` to restore.

Targets MySQL 8.0 and later (including 8.4), MariaDB (tested on 10.11 and 11.4; see [MariaDB](#mariadb)), MySQL 5.7 (tested on 5.7.44 only; see [MySQL 5.7](#mysql-57)) and MySQL 5.6 (tested on 5.6.51 only; see [MySQL 5.6](#mysql-56)). MySQL 5.5 and earlier are not supported. [Compatibility](#compatibility) lists which server, output format and restore destination were actually tried.

## Compatibility

Every cell says how far it is backed, not how likely it is to work. A cell is **Tested** only when an automated test or a recorded manual check (design document, section 11) covers it.

| Mark | Meaning |
| --- | --- |
| **Tested** | An integration test or a recorded manual check passes. |
| **Conditional** | It works for what was tried, with a stated condition or a part not tried. |
| **Fails** | A failure was seen on a real server. The note says why. |
| **Unverified** | Not tried. Do not assume it works, or that it fails. |

### Source server and `--target`

The source is the server `mysqldance` reads. The cells say whether the output was produced and restored into a server of that kind (`--target mysql` into the same server, `sqlite` through `modernc.org/sqlite`, `postgres` into PostgreSQL 17) with every row compared afterwards.

| Source | `--target mysql` | `--target sqlite` | `--target postgres` |
| --- | --- | --- | --- |
| MySQL 8.4 | Tested | Tested | Tested (PostgreSQL 14 to 18) |
| MySQL 8.0 | Tested | Tested | Tested |
| MySQL 5.7.44 ([notes](#mysql-57)) | Tested | Tested | Tested |
| MariaDB 10.11 and 11.4 ([notes](#mariadb)) | Tested | Tested | Tested |
| MySQL 5.6.51 ([notes](#mysql-56)) | Tested | Unverified | Unverified: a zero date, common in 5.6, stops it |
| MySQL 5.5 and earlier | Unverified: refused (support is tracked as B-080) | Unverified | Unverified |
| MySQL 9.x and other MySQL-compatible servers (TiDB and so on) | Unverified | Unverified | Unverified |

Notes:

- Only the versions named were run; other releases of the same series are unverified (5.7.21 only for the session settings).
- `--target postgres` stops on a value PostgreSQL cannot hold (a zero date, a NUL byte), whatever the source; zero dates are common in old servers. See [PostgreSQL output](#postgresql-output).
- MySQL 5.6.51 is tested with `--target mysql` only. Its `--target sqlite` and `--target postgres` output was made and compared with 5.7.44's by hand (design document, section 11, B-079), but no test restores it, so those cells stay unverified.

### Output and restore destination

`--target mysql` output (the rows are the source; the columns are the server the files are restored into):

| Output from | into MySQL 5.7.44 | into MySQL 8.0 | into MySQL 8.4 | into MariaDB 10.11 / 11.4 |
| --- | --- | --- | --- | --- |
| MySQL 8.4 | Unverified | Unverified | Tested | Fails: `utf8mb4_0900_ai_ci` does not exist in MariaDB |
| MySQL 8.0 | Unverified | Tested | Unverified | Unverified (same default collation as 8.4) |
| MySQL 5.7.44 | Tested | Unverified | Conditional: zero-date defaults restore only because of the `SET SQL_MODE` in each file's header; 8.0 was not tried | Unverified |
| MySQL 5.6.51 | Unverified | Unverified | Conditional: zero dates and zero-date defaults restore only because of the `SET SQL_MODE` in each file's header; 8.0 was not tried | Tested (10.11; 11.4 not tried) |
| MariaDB 10.11 | Unverified | Unverified | Unverified | Tested into the same version; 11.4 not tried |
| MariaDB 11.4 | Unverified | Unverified | Fails: `utf8mb4_uca1400_ai_ci` does not exist in MySQL | Tested into the same version; 10.11 not tried |

MariaDB and MySQL outputs are not converted for each other (see [MariaDB](#mariadb)).

`--target sqlite` and `--target postgres` output:

| Output | Restored into | Status |
| --- | --- | --- |
| `sqlite` | `modernc.org/sqlite` v1.62.0 (SQLite 3.54.1; what the tests use) | Tested |
| `sqlite` | `sqlite3` command 3.46.1 | Tested (from MySQL 8.0 and 8.4) |
| `sqlite` | `sqlite3` command 3.8.2 | Tested (from `sample_main` of MySQL 8.x, recorded by hand) |
| `sqlite` | `sqlite3` command 3.7.16 to 3.8.1 | Unverified (3.7.16 is the minimum by SQLite's release notes) |
| `sqlite` | `sqlite3` command 3.7.9 | Fails: the multi-row `INSERT` is a syntax error |
| `postgres` | PostgreSQL 14, 15, 16, 17, 18 | Tested (from MySQL 8.4; PostgreSQL 17 also from 8.0, 5.7.44 and MariaDB) |
| `postgres` | PostgreSQL 13 and earlier | Unverified |
| `sqlite` / `postgres` | Other SQLite-compatible or PostgreSQL-compatible DBMSs (libSQL, Cloudflare D1, CockroachDB and so on) | Unverified |

When the supported servers or restore destinations change, update these tables in the same change.

## Build

```sh
go build ./cmd/mysqldance
```

To build release-style binaries for every supported OS and architecture (linux, darwin and windows on amd64 and arm64) into `dist/`, run `task snapshot` (requires [GoReleaser](https://goreleaser.com)).

Pushing a tag that starts with `v` (for example `v1.0.0`) runs the `tag` workflow, which publishes a GitHub Release with the archives, `checksums.txt` and a changelog, and pushes the container image (below).

### Container image

A multi-architecture image (linux/amd64 and linux/arm64) is published to `ghcr.io/wtnb75/mysqldance`. It is built on `gcr.io/distroless/static:nonroot`, so it has no shell and runs as a non-root user; the entrypoint is `mysqldance`.

| Tag | Content |
| --- | --- |
| `<version>` (for example `1.0.0`) | The release with that tag |
| `latest` | The newest release that is not a prerelease |
| `main` | The latest commit on `main` (unreleased) |

```sh
docker run --rm --add-host=host.docker.internal:host-gateway -e MYSQL_PWD -v ./dump:/dump ghcr.io/wtnb75/mysqldance:latest \
  -h host.docker.internal -u root -o /dump mydb
```

The container runs as a non-root user (uid 65532), so the output directory you mount must be writable by it. Use `--network host` or any address the container can resolve to reach the server. Pushes to other branches build the image and run `--version` in CI but do not publish it.

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
| `--password-file` | (none) | File containing the password. One trailing newline is stripped. A warning is logged if group or others can read it |
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
| `--connect-timeout` | `30s` | How long to wait for the first connection to the server to be made (the TCP connect, the server's greeting and the login), as a duration. When it runs out, the run fails before anything is written. `0` sets no limit of its own (the TCP connect is then left to the operating system) |
| `--read-timeout` | `0` | How long to wait for each response from the server, as a duration. A server that stops answering then fails the run instead of stalling it (the driver logs `i/o timeout` to standard error; when it happens while the run is starting, the error also names the read timeout). `0` sets no limit, so that a normal read of a large table is never cut off. It is a limit on one wait, not on the whole transfer, but it also covers a statement that sends nothing until it is done: a sort of a large table, and the wait for `FLUSH TABLES WITH READ LOCK`. A value below `--lock-timeout` therefore ends that wait first, so keep it well above both |
| `--sort-memory` | `64MiB` | About how much memory a table without a primary key or NOT NULL unique key may use while its rows are sorted, from `1MiB` up (`64MiB`, `1GiB`, or a plain number of bytes). Past that, the sorted batches go to temporary files in the output directory and are merged (see "Limitations"). With `--jobs N`, N tables are sorted at once, so the total is up to N times this |
| `-j`, `--jobs` | `1` | Number of tables read in parallel, each on a connection of its own, from 1 to 64. The default `1` reads everything on one connection, as before, and needs no `RELOAD` privilege. Above `1` the global read lock is always taken, so the account needs `RELOAD`, and `--no-lock` cannot be combined with it (see "Parallel reads" below) |
| `--log-level` | `info` | Lowest level of the log records written to standard error: `debug`, `info`, `warn` or `error`. Warnings are `warn` records, so `error` hides them (see "Warnings" below) |
| `--log-format` | `text` | Format of the log records: `text` (`key=value`, as `log/slog`'s text handler writes it) or `json` (one JSON object per line). The error that ends a failed run is an `ERROR` record too (`msg="dump failed"`, the message in the `error` attribute), so it follows both flags; the exit code is 1 and nothing goes to standard output. A mistake in the flags themselves is still printed as a plain `mysqldance: ...` line |
| `--version` | off | Print the version and commit to standard output, then exit with 0. Builds made without the release ldflags use the build info Go recorded: the module version for `go install ...@vX.Y.Z`, and the VCS revision (with `-dirty` for a modified tree) for `go build` in a checkout. Without VCS information they print `dev` and `unknown` |
| positional | (at least one required) | Names of the databases to dump |

- The password is never accepted on the command line. If `--password-file` is given its contents are used; otherwise the `MYSQL_PWD` environment variable is used.
- Prefer `--password-file` for anything but a throwaway run. On some systems the environment of a process can be read by other processes of the same host (for example through `/proc/<pid>/environ` or `ps e`), and it is inherited by every child process of the shell that exported it. A password file is not, and mysqldance warns when one is readable by group or others (see "Warnings").
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
| `<db>.15-routines.sql` | Only for a database that has stored procedures or functions (see "Stored routines"): the fixed header, then `DROP PROCEDURE` / `DROP FUNCTION IF EXISTS`, `SET SQL_MODE` and `CREATE` for each routine |
| `<db>.20-triggers.sql` | Only for a database that has triggers (see "Triggers"): the fixed header, then `DROP TRIGGER IF EXISTS`, `SET SQL_MODE` and `CREATE TRIGGER` for each trigger |
| `<db>.25-events.sql` | Only for a database that has events (see "Events"): the fixed header, then `DROP EVENT IF EXISTS`, `SET SQL_MODE`, `SET TIME_ZONE` and `CREATE EVENT` for each event |

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

### Stored routines

A database with stored procedures or functions also gets `<db>.15-routines.sql` (a database without any gets no such file, and one left from an earlier dump is removed). The name sorts after every data file and before `<db>.20-triggers.sql`, because a trigger may call a routine.

- Each routine is written as `DROP PROCEDURE IF EXISTS` (or `DROP FUNCTION IF EXISTS`), `SET SQL_MODE='...'` and the statement of `SHOW CREATE PROCEDURE` / `SHOW CREATE FUNCTION`. The `sql_mode` is the one the routine was created under, because it changes how the body is parsed.
- **The `DEFINER=...` clause is not written**, as for views and triggers: the routines are created as the user who restores the dump. A routine with `SQL SECURITY DEFINER` (the default) then runs with the privileges of that user.
- Bodies contain `;`, so the statements end with a delimiter set by `DELIMITER`, chosen and restored exactly as for triggers (see "Triggers"): the file restores with the `mysql` client, which is what `docker-entrypoint-initdb.d` runs. A routine with a line that starts with `DELIMITER` fails the run.
- Procedures come first, then functions, each by name in byte order. A routine does not depend on another at creation time (the server does not look up what a body calls), so the order only keeps the output the same every run.
- The character set, collation and database collation recorded with a routine are not kept: the file runs with `SET NAMES utf8mb4`, and a string parameter without its own character set takes the default of the restored database.
- A routine whose statement the server lists but returns as `NULL` (the account lacks `SHOW_ROUTINE`, or `EXECUTE` / `ALTER ROUTINE` on it) is an error, not a silent omission. A routine the server does not list at all for lack of privileges is not seen, so it is left out without a message; this is not checked yet (backlog B-110, the same for triggers).
- Restoring needs the right to create them. With binary logging on (the MySQL 8 default), a function that is not declared `DETERMINISTIC`, `NO SQL` or `READS SQL DATA` fails with error 1418 unless the restoring user has `SUPER` or `log_bin_trust_function_creators` is `1` (MySQL's documented rule for stored functions; not tried here).
- `--target sqlite` and `--target postgres` do not write routines (their bodies are MySQL syntax); a warning names each one.
- Verified by restoring and comparing the definitions on MySQL 8.0 and 8.4 only; MySQL 5.7, 5.6 and MariaDB are not tried for routines.

### Triggers

A database with triggers also gets `<db>.20-triggers.sql` (a database without any gets no such file, and one left from an earlier dump is removed). The name sorts after every data file, so the triggers are created once all rows are in and loading fires none of them; it sorts before `<db>.90-constraints.sql`.

- Each trigger is written as `DROP TRIGGER IF EXISTS`, `SET SQL_MODE='...'` and the statement of `SHOW CREATE TRIGGER`. The `sql_mode` is the one the trigger was created under, because it changes how the body is parsed (`ANSI_QUOTES`, for one).
- **The `DEFINER=...` clause is not written**, as for views: the triggers are created as the user who restores the dump, and the name of the dumping user stays out of the output.
- Bodies may contain `;`, so the statements end with a delimiter set by `DELIMITER` (`$$`; if a statement contains it, or ends with a character that would join it, the next of `//`, `##`, `%%`, `!!`, `$$$` and so on). A trigger with a line that starts with `DELIMITER` fails the run, because the client would run that line as a command. `DELIMITER` is a command of the `mysql` client: the file restores with the client (which is what `docker-entrypoint-initdb.d` runs), not through a driver that sends the whole file as one multi-statement query.
- Triggers are ordered by table, timing (`AFTER` before `BEFORE`) and event, then in the order the server runs them (their creation order), then by name. Triggers of the same table, timing and event are therefore created in the order they run in, so the restored ones run in that order too.
- The character set and collation of the connection the trigger was created with are not kept: the file runs with `SET NAMES utf8mb4`.
- A trigger whose statement cannot be read (the account lacks the `TRIGGER` privilege) is an error, not a silent omission.
- `--target sqlite` and `--target postgres` do not write triggers (their bodies are MySQL syntax); a warning names each one.

### Events

A database with events also gets `<db>.25-events.sql` (a database without any gets no such file, and one left from an earlier dump is removed). The name sorts after every data file, the routines file and the triggers file, because an event body may call a routine or fire a trigger, and before `<db>.90-constraints.sql`.

- Each event is written as `DROP EVENT IF EXISTS`, `SET SQL_MODE='...'`, `SET TIME_ZONE='...'` and the statement of `SHOW CREATE EVENT`. The `sql_mode` and the time zone are the ones the event was created under: the mode changes how the body is parsed, and the server keeps the session's time zone with the event, so its schedule (`AT`, `STARTS`, `ENDS`) is read in that zone and its body runs in it. The zone is written as the server reports it (`SYSTEM`, an offset such as `+09:00`, or a name such as `Asia/Tokyo`); a name needs the time zone tables loaded in the server that restores the dump.
- **An event keeps the status it has** (`ENABLE`, `DISABLE` or `DISABLE ON SLAVE`). An enabled event is therefore live as soon as the restored server's event scheduler is on, which it is by default in MySQL 8 and so also while `docker-entrypoint-initdb.d` restores: one whose schedule is already due can run during the restore itself, or right after it. To restore without running any event, turn the scheduler off on the restoring server (`event_scheduler=OFF`, for instance `docker run ... mysql --event-scheduler=OFF`) and turn it on when the restore is done. Events are never altered by the dump to avoid this, so the restored definitions equal the source's.
- A one-time event (`AT`) whose time is already past when it is restored is not kept as it was: the server drops it when it is `ON COMPLETION NOT PRESERVE`, and disables it (with a warning) when it is `PRESERVE`. This is the server's rule, not a choice of the dump.
- **The `DEFINER=...` clause is not written**, as for views, triggers and routines: the events are created, and run, as the user who restores the dump. That user needs the `EVENT` privilege on the database. An event has no `SQL SECURITY` clause and always runs with its definer's privileges, so restoring as an administrator makes every event run with those privileges, which can be more than the source's definer had (keeping the definer is backlog B-111).
- Bodies may contain `;`, so the statements end with a delimiter set by `DELIMITER`, chosen and restored exactly as for triggers (see "Triggers"): the file restores with the `mysql` client. An event with a line that starts with `DELIMITER` fails the run.
- Events are ordered by name in byte order. They do not depend on each other, so the order only keeps the output the same every run.
- The character set and collation of the connection the event was created with are not kept: the file runs with `SET NAMES utf8mb4`.
- An event whose statement the server lists but returns as `NULL` is an error, not a silent omission. An event the server does not list at all for lack of the `EVENT` privilege is not seen, so it is left out without a message (the same gap as for triggers and routines, backlog B-110).
- `--target sqlite` and `--target postgres` do not write events (their bodies are MySQL syntax); a warning names each one.
- Verified by restoring and comparing the definitions on MySQL 8.0 and 8.4 only; MySQL 5.7, 5.6 and MariaDB are not tried for events.

### SQLite output

`--target sqlite` writes the same files (same names, same one-record-per-line `INSERT`s, same splitting and determinism) in SQL that SQLite runs. The files of one MySQL database restore into one SQLite database; run them in lexicographic file name order:

```sh
LC_ALL=C cat dump/mydb.*.sql | sqlite3 mydb.db
```

- The schema file has no `CREATE DATABASE`, `USE` or `SET`: it starts with `PRAGMA foreign_keys=OFF;`, then one `CREATE TABLE` per table (by name), each followed by its `CREATE [UNIQUE] INDEX` statements. The table definitions are built from `information_schema`, not from `SHOW CREATE TABLE`. A data file runs in one transaction (`PRAGMA foreign_keys=OFF; BEGIN; ... COMMIT;`).
- Column types keep every value exact. Integer types, `YEAR` and `BIT(n)` with `n` up to 63 become `INTEGER`; `FLOAT` and `DOUBLE` become `REAL`; binary and spatial types become `BLOB`. `DECIMAL`, `BIGINT UNSIGNED` and `BIT(64)` become `TEXT`, because SQLite would store a number above 2^63-1 or a decimal as a lossy `REAL`; their values are written as strings. All other types (strings, `ENUM`, `SET`, `JSON`, dates and times) become `TEXT`. `NOT NULL`, the primary key, foreign keys (with `ON DELETE` / `ON UPDATE`), `UNIQUE` keys and ordinary indexes (with `DESC`) are carried over, and so are literal defaults and `DEFAULT CURRENT_TIMESTAMP`.
- **A foreign key whose referenced columns are not a key is not output.** InnoDB accepts any index on the referenced columns, but SQLite needs them to be exactly the parent's primary key or a unique index (in any column order); otherwise writing to the child table fails with `foreign key mismatch` once foreign keys are turned on. Such a foreign key is **not output**, with a warning that names it and the parent table. The check reads the parent table's definition; a foreign key to a table that was not read is output as it is. A foreign key into another database is not output either.
- Index names are made unique in the database as `<table>__<index>`; if that collides with another table or index name, the run fails.
- A line break inside a string value is written as `'a'||char(10)||'b'`, so one record stays on one line.
- The files need `sqlite3` 3.7.16 or newer: they use multi-row `VALUES` (3.7.11) and `char()` (3.7.16), according to SQLite's release notes. The restore is verified with `sqlite3` 3.46.1 and 3.8.2 only; 3.7.9 rejects the `INSERT`s, and 3.7.16 to 3.8.1 were not run. See [Compatibility](#compatibility).
- MySQL's `AUTO_INCREMENT` is not carried over (as in the MySQL output, the counter is not restored). Table and column comments, character sets and collations are dropped; text compares as plain bytes in SQLite, which is never stricter than MySQL's collations, so the data always loads.
- **Not output, with a warning for each** (a `WARN` record on standard error with the attribute `database=db`, see "Warnings"; the dump continues): generated columns (their values are not dumped either), `ON UPDATE CURRENT_TIMESTAMP`, default expressions other than `CURRENT_TIMESTAMP`, `FULLTEXT` and `SPATIAL` indexes, indexes with an expression key part or on a generated column, CHECK constraints, foreign keys into another database, and the views that cannot be converted (below). A prefix index (`col(10)`) is created on the whole column, which is also reported. Triggers, stored routines and events are not output either, with a warning for each (their bodies are MySQL syntax).
- **Views are converted by an allow list.** A view's `SELECT` is MySQL syntax, so it is carried over only if every word in it is known to mean the same in SQLite. The `ALGORITHM`, `DEFINER` and `SQL SECURITY` clauses and the character set introducers (`_utf8mb4'x'`) are dropped. Columns, `JOIN` (`INNER`, `LEFT`, `CROSS`), `WHERE`, `GROUP BY`, `HAVING`, `ORDER BY`, `LIMIT`, `UNION`, subqueries, `CASE`, `IN`, `LIKE`, `BETWEEN`, and the functions `COUNT`, `SUM`, `MIN`, `MAX`, `AVG`, `COALESCE`, `IFNULL`, `NULLIF`, `ABS`, `ROUND`, `UPPER`, `LOWER`, `SUBSTR`, `TRIM` and `REPLACE` pass as they are; `IF(a,b,c)` becomes `CASE WHEN a THEN b ELSE c END` and `CONCAT(a,b)` becomes `(a || b)` (an argument that is more than a name or a call is parenthesized). `COUNT(DISTINCT a, b)` is not output, as SQLite takes one expression there. The views are written after the tables, in dependency order. A view with anything else (another function such as `DATE_FORMAT()` or `NOW()`, `WITH CHECK OPTION`, an operator such as `<=>`, a window function, a read of another database) is **not output**, with a warning that names the reason; so is a view that reads a view that is not output. A view that is output but contains something whose result can differ from MySQL (a string literal, `LIKE`, `GROUP BY`, `ORDER BY`, `DISTINCT`, `UNION`, `/`, `%`, `SUM()`, `AVG()`, `ROUND()`, `UPPER()`, `LOWER()`, `SUBSTR()`) gets a warning that says so: SQLite compares and sorts text byte by byte (MySQL's collations are usually case-insensitive) and divides two integers as integers. SQLite does not look at the tables a view reads until the view is queried, so a view that reads a column that was not output (a generated column) restores, and fails when it is queried.
- A column type with no SQLite counterpart makes the run fail instead of guessing.

### PostgreSQL output

`--target postgres` writes the same one-record-per-line `INSERT`s, with the same splitting and determinism, in SQL that PostgreSQL runs. One MySQL database becomes one **schema** of the same name, so several databases fit into one PostgreSQL database. The output was restored into PostgreSQL 14 to 18; see [Compatibility](#compatibility). For each database, three kinds of files are written:

| File | Content |
| --- | --- |
| `<db>.00-schema.sql` | `CREATE SCHEMA IF NOT EXISTS`, then one `CREATE TABLE` per table (by name), each followed by its `CREATE [UNIQUE] INDEX` statements |
| `<db>.10-data.<table>.sql` | The table's `INSERT` statements, in one transaction |
| `<db>.90-constraints.sql` | The foreign keys (`ALTER TABLE ... ADD CONSTRAINT`), always written, with no statement when there is none |

The foreign keys are in the last file because the data files run in table-name order, which no foreign key respects. Every file sets what it needs itself (`client_encoding`, `standard_conforming_strings`, `TimeZone = 'UTC'`, `search_path`), because the official image runs each file in a session of its own, so they restore with the official image's `/docker-entrypoint-initdb.d` (or `psql -f` on each file in file name order) into an empty database. Restoring into a schema that already has the tables fails.

- **Types** keep every value exact. `TINYINT(1)` (`BOOLEAN`) becomes `boolean`; every other `TINYINT`, `SMALLINT`, `MEDIUMINT`, `INT`, `BIGINT` and `YEAR` becomes the narrowest PostgreSQL integer type that holds the whole range (`TINYINT UNSIGNED` is `smallint`, `INT UNSIGNED` is `bigint`, `BIGINT UNSIGNED` is `numeric(20,0)`). `DECIMAL(p,s)` is `numeric(p,s)`, `FLOAT` is `real`, `DOUBLE` is `double precision`, `BIT(n)` is `bit(n)`, `CHAR(n)` and `VARCHAR(n)` are `varchar(n)` (PostgreSQL's `char(n)` pads, MySQL's does not), the text types, `ENUM` and `SET` are `text`, `JSON` is `json`, binary and spatial types are `bytea`, `DATE` is `date`, `DATETIME(n)` is `timestamp(n)`, `TIMESTAMP(n)` is `timestamptz(n)` and `TIME(n)` is `interval(n)` (its range, -838:59:59 to 838:59:59, is wider than a time of day).
  - Only a plain `TINYINT(1)` is `boolean`, which is what MySQL 8.0.19 and later reports as `tinyint(1)`; `TINYINT(1) UNSIGNED` is reported as `tinyint unsigned` and becomes `smallint`. A `TINYINT(1)` row holding anything but 0 or 1 makes the run fail.
- `NOT NULL`, the primary key, `UNIQUE` keys, ordinary indexes (with `DESC`) and literal defaults (and `DEFAULT CURRENT_TIMESTAMP`) are carried over. Primary keys and indexes are named `<table>__<index>` (the primary key is `<table>__PRIMARY`); if that collides with another table or index name, the run fails. MySQL allows 64 characters in each of the table and index names, so `<table>__<index>` can pass PostgreSQL's 63-byte limit. Such a name is shortened, with a warning that gives both names: it keeps the start of the name (cut between characters, never inside a UTF-8 sequence) and ends in `_` and the first 8 hex digits of the SHA-256 of the whole `<table>__<index>`, 63 bytes at most. A name within the limit is never changed, and the same input always gives the same name. If a shortened name collides with another name, the run fails.
- A foreign key name is allowed 64 characters in MySQL, so it can pass the limit too. It is shortened by the same rule (applied to the name alone, with a warning that gives both names), for a foreign key that is output. PostgreSQL keeps constraint names unique within a table, so a shortened name that equals another foreign key's name, or the table's primary key name (`<table>__PRIMARY`), fails the run.
- A line break inside a string value is written as an `E'a\nb'` string, so one record stays on one line.
- MySQL's `AUTO_INCREMENT` is not carried over: the column is a plain integer, the counter is not restored (as in the MySQL output), and a later `INSERT` has to give the key. Comments, character sets and collations are dropped.
- **A value or name PostgreSQL cannot hold fails the run** with the table and column named: a NUL byte in text, a zero or partial date (`0000-00-00`, `2024-00-10`), a table, column or database name over 63 bytes (an index or foreign key name is shortened instead, see above), a database name starting with `pg_`, a column type with no PostgreSQL counterpart.
- **Not output, with a warning for each** (a `WARN` record on standard error with the attribute `database=db`, see "Warnings"; the dump continues): generated columns (their values are not dumped either), `ON UPDATE CURRENT_TIMESTAMP`, default expressions other than `CURRENT_TIMESTAMP`, `FULLTEXT` and `SPATIAL` indexes, indexes with an expression key part or on a generated column, CHECK constraints, the views that cannot be converted (below), and foreign keys that PostgreSQL cannot take: into another database (the constraints file of one database runs before the schema file of a later one), onto columns that are not a primary or unique key (InnoDB accepts any index, PostgreSQL does not), or onto a generated column. A prefix index (`col(10)`) is created on the whole column, which is also reported. Triggers, stored routines and events are not output either, with a warning for each (their bodies are MySQL syntax).
- **Views are converted by an allow list, as for SQLite** (see the SQLite section for the words that pass). The `ALGORITHM`, `DEFINER` and `SQL SECURITY` clauses and the character set introducers are dropped; identifiers are quoted with `"`; `IF(a,b,c)` becomes `CASE WHEN a THEN b ELSE c END`, `IFNULL` becomes `COALESCE`, and `CONCAT(a,b)` becomes `(CAST(a AS text) || CAST(b AS text))` (a string literal is not cast); `LIMIT n,m` becomes `LIMIT m OFFSET n`. `ROUND()` is not converted (PostgreSQL rounds only `numeric` to a number of digits). The views are written at the end of the schema file, in dependency order. PostgreSQL checks a view when it is created, and a view it refuses would stop the restore, so the shapes that are certain to be refused are **not output**, with a warning that names the reason: a boolean column (`TINYINT(1)`) used as a number (compared with a number, in arithmetic, or as an argument of `SUM()`, `MIN()`, `CONCAT()` and the like; MySQL prints `WHERE flag` as `flag <> 0`, so that is refused too), and a `SELECT` of a column that is not in the `GROUP BY` (PostgreSQL accepts that only for the columns of a primary key that is wholly grouped; subqueries are not checked), a read of a generated column (it is not output), and a `LIMIT` number that PostgreSQL's `bigint` cannot hold (MySQL's `LIMIT n,18446744073709551615`). The other cases of the SQLite section (another function, `WITH CHECK OPTION`, a read of another database, a view that reads a view that is not output) and a view whose name is already used by a table or an index are not output either. A view that is output but may give another result than in MySQL (a string literal, `LIKE`, `GROUP BY`, `ORDER BY`, `DISTINCT`, `UNION`, `/`, `%`, `SUM()`, `AVG()`, `UPPER()`, `LOWER()`, `SUBSTR()`) gets a warning that says so: PostgreSQL compares and sorts text by its own collation (the collations of MySQL are not carried over), puts `NULL` last in an ascending order, and divides two integers as integers. A mismatch of types that cannot be seen from the text (for example `UPPER()` of a number, which MySQL accepts) is not found, and the view fails when the file is restored. Checked on PostgreSQL 14 to 18.

### MariaDB

A server that reports itself as MariaDB is read as MariaDB. It is not rejected by version: 10.11 and 11.4 are the versions it was tested on, and other versions are untested. [Compatibility](#compatibility) shows which combinations of source, output and restore destination were tried.

- **The output is MariaDB's own.** `CREATE TABLE` and `CREATE VIEW` are written as MariaDB's `SHOW CREATE` prints them (for example `current_timestamp()`, `int(11)`, `JSON` as `longtext` with a `CHECK`, and `CREATE DATABASE` with MariaDB's default collation). Restore the default `--target mysql` output of a MariaDB server into MariaDB. **It is not meant for MySQL**: for example, 11.4's default collation `utf8mb4_uca1400_ai_ci` does not exist in MySQL, and MySQL's `utf8mb4_0900_ai_ci` does not exist in MariaDB, so the statement fails on the other server. No conversion is made between the two.
- **Views** are ordered by their dependencies, which are read from `information_schema.VIEWS` (MariaDB has no `VIEW_TABLE_USAGE`). The account needs the `SHOW VIEW` privilege (it already does, for `SHOW CREATE VIEW`).
- **Sequences and system-versioned tables stop the run.** A `SEQUENCE` and a table `WITH SYSTEM VERSIONING` (`information_schema.TABLES.TABLE_TYPE` `SEQUENCE` / `SYSTEM VERSIONED`) are not output, and a plain `SELECT` would lose the history rows of the latter, so the run fails before any file is changed (see [Failure handling](#failure-handling)) and names the table. Drop it, or dump from a database without it.
- **Defaults** are read as MariaDB reports them, so `--target sqlite` and `--target postgres` get the same `DEFAULT` clauses as from MySQL (`current_timestamp()` becomes `CURRENT_TIMESTAMP`; other expression defaults are left out with a warning).

### MySQL 5.7

A server whose version starts with `5.7.` is read as MySQL 5.7. **Only 5.7.44 was tested** (the session settings were also tried on 5.7.21); other 5.7 releases are untested. MySQL 5.6 is read the same way (see [MySQL 5.6](#mysql-56)); 5.5 and earlier are refused. [Compatibility](#compatibility) shows which combinations of source, output and restore destination were tried.

- **The output is 5.7's own.** `CREATE TABLE` is written as 5.7's `SHOW CREATE` prints it: integers keep their display width (`int(11)`, `bigint(20) unsigned`), and a database or table made with the stock `latin1` default says `DEFAULT CHARSET=latin1`. No conversion is made. Rows are read as `utf8mb4`, so the data of a `latin1` column is exact. The default `--target mysql` output restores into 5.7, and into MySQL 8.4 for the schemas tested; a zero-date default such as `DEFAULT '0000-00-00 00:00:00'` (a second `TIMESTAMP NOT NULL` column when `explicit_defaults_for_timestamp` is `OFF`, the 5.7 default) restores into 8.4 as well (8.0 was not tried), although 8.4's own `sql_mode` refuses it with `Invalid default value`: the `SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO'` in every file's header replaces the session's mode, which drops `NO_ZERO_DATE` and strict mode. A restore that skips the header (pasting only the `CREATE TABLE`) fails on 8.x.
- **`show_create_table_verbosity` is not set.** 5.7.21 and earlier do not have it, and the version is not told apart. On 5.7.22 and later a server with the variable set to `ON` globally adds the default `ROW_FORMAT` to `SHOW CREATE TABLE`, so the output follows that setting (this is untested).
- **Views** are ordered by their dependencies, which are read from `information_schema.VIEWS` (5.7 has no `VIEW_TABLE_USAGE`), as for MariaDB. The account needs the `SHOW VIEW` privilege.
- **Defaults** `CURRENT_TIMESTAMP` and `CURRENT_TIMESTAMP(n)` of a `TIMESTAMP` or `DATETIME` column are expressions, so `--target sqlite` and `--target postgres` get `DEFAULT CURRENT_TIMESTAMP` as from 8.x. The same text in a string column stays a string.
- **Foreign keys** without `ON DELETE` / `ON UPDATE` are reported by 5.7 as `RESTRICT`, and by 8.x as `NO ACTION`. `--target sqlite` and `--target postgres` write the word the server reports, so the same schema gives `RESTRICT` from 5.7 and `NO ACTION` from 8.x. Both load in SQLite and PostgreSQL, and they differ only in when PostgreSQL checks them (not with a deferred constraint).
- 5.7 has no `INVISIBLE` columns, expression keys or `CHECK` constraints (a `CHECK` is parsed and dropped by the server), and it ignores `DESC` in an index, so none of them is in the output.
- **TLS:** the official 5.7 image offers only RSA key exchange cipher suites, which Go 1.22 and later do not enable by default, so the default `--ssl-mode preferred` fails with `tls: handshake failure` against it (it does not fall back to clear text). Use `--ssl-mode disabled`, or run `mysqldance` with `GODEBUG=tlsrsakex=1` (the integration tests do). The 5.7.21 image offers TLS 1.1 at most, below the TLS 1.2 minimum, so it needs `--ssl-mode disabled`.

### MySQL 5.6

A server whose version starts with `5.6.` is read as MySQL 5.7 is: the same catalog queries, session settings and view ordering (see [MySQL 5.7](#mysql-57)), with no code of its own. **Only 5.6.51 was tested** (the last release of the official image); other 5.6 releases are untested, and 5.5 and earlier are refused with `MySQL 5.6 or later (or MariaDB) is required`. [Compatibility](#compatibility) shows which combinations of source, output and restore destination were tried.

- **The output is 5.6's own**, as for 5.7: `int(11)`, `DEFAULT CHARSET=latin1` for the stock default, no conversion. `DATETIME(n)` and `TIMESTAMP(n)` (5.6.4 and later) and `DEFAULT CURRENT_TIMESTAMP(n)` come out as on 5.7. The default `--target mysql` output restores into MySQL 8.4 and MariaDB 10.11 for the schemas tested.
- **Zero dates are common.** 5.6's default `sql_mode` is not strict, so `0000-00-00` and `2020-00-10` can be stored, and with `explicit_defaults_for_timestamp` `OFF` (the 5.6 default) the second `TIMESTAMP NOT NULL` column gets `DEFAULT '0000-00-00 00:00:00'` by itself. `--target mysql` and `--target sqlite` write them as they are, and the `SET SQL_MODE` in each file's header lets them restore into a strict 8.4 or MariaDB (as for 5.7). **`--target postgres` fails** on a zero date (`is not a date and time PostgreSQL accepts`) and leaves a zero-date default out with a warning.
- **TLS:** the official 5.6 image starts with TLS disabled (it makes no certificate), so the default `--ssl-mode preferred` **silently falls back to clear text**, and the password and every row travel unencrypted. Use `--ssl-mode required` to make that a failure. A 5.6.51 server given a certificate (`--ssl-ca`, `--ssl-cert`, `--ssl-key` on the server) connects with TLS 1.2, the minimum `mysqldance` asks for; earlier 5.6 releases may not offer it.
- 5.6 has no generated columns, `JSON`, `INVISIBLE` columns, expression keys or `CHECK` constraints, ignores `DESC` in an index, and allows one trigger for each event of a table.
- `show_create_table_verbosity` is not set (5.6 does not have it), as for 5.7.

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

- **255-byte limit**: a non-ASCII name grows to roughly 9 bytes per character, so a file name can pass 255 bytes. Only then, the longer of the escaped database part and the table part is shortened: its head is kept (never cut inside a `%XX`), followed by `~` and 16 hex digits of the SHA-256 of the whole escaped name (`~` is always escaped in a real name, so a shortened name cannot equal an unshortened one). A database part is shortened to 100 bytes and is the same in every file of the database, so the files still sort together, schema first; the table part is then shortened as far as the rest of the name needs (the part number of a split table counts too). File names within the limit do not change, and neither does anything inside the files. Each shortening is logged as a warning with the original and the shortened name. The run fails, and the output directory is left unchanged, only if shortened names collide (see below) or a name cannot be brought within 255 bytes.
- **Names differing only in case**: if two output file names become identical when lowercased, such as `Users` and `users` (across all databases in one run), the run fails. This prevents files from overwriting each other on case-insensitive file systems.

## Determinism and ordering

- For the same data, every file is byte-for-byte identical across runs.
- Row order: primary key; if there is none, the first NOT NULL unique key (by index name in lexicographic order); if there is neither, the byte order of each row's output string. String keys are compared as binary, not by collation.
- `INSERT` statement boundaries are decided by row content (a hash), so updating one row normally changes just that row in the diff (one deletion plus one addition). Adding or deleting rows only changes the area around them. The hash is FNV-1a followed by a mixing step, so statements average `--rows-per-insert` rows for any value, even or odd, and for rows with a common tail. The mixing step moved the `INSERT` boundaries for every `--rows-per-insert` value, so the first dump after the upgrade differs from an older one in where statements start and end (a one-time large diff); later dumps are stable.
- Value formats: strings are `'...'` (with `\`, `'`, `"`, newlines, NUL and so on escaped); `BLOB`, `BINARY`, `VARBINARY` and `GEOMETRY` are `0x` followed by hex (empty is `''`); `BIT` is `b'...'`; NULL is `NULL`. Times are read and written with `TIME_ZONE='+00:00'`.
- `FLOAT` and `DOUBLE` come back with the same bits after a restore, `-0` included. A `DOUBLE` is written as the server prints it, except that `-0` is written `-0e0` (a bare `-0` is the integer 0 negated, which loses the sign). The server prints a `FLOAT` with only 6 significant digits, which loses bits, so a `FLOAT` is read as its exact `DOUBLE` value (`col * 1e0`) and written in the shortest notation that gives the same single-precision value: `0.1` stays `0.1`, and a value that needs more digits gets them (`0.33333334`, where an earlier version wrote `0.333333`). Large and small values use an exponent (`16777216` is `1.6777216e+07`; an earlier version wrote the server's `16777200`). The first dump after the upgrade therefore differs from an older one in the cells of such `FLOAT` values and `-0`; later dumps are stable. In a table with no key, rows are ordered by their output text, so rows with such a `FLOAT` may also move once.
- The `DEFINER=...` clause of views, triggers, stored routines and events is not written (they are created as the restoring user).
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
- **Memory:** a table with a primary key or a NOT NULL unique key is streamed (see "Limitations"), so N tables in parallel need no more memory than one. A table without a key is sorted with at most `--sort-memory` of rows in memory (the rest goes to temporary files), so up to N such tables use up to N times that at once.
- **Server connections:** N + 1, plus the ones the server needs for itself; `max_connections` must allow them. The upper bound of `--jobs` is 64.
- DDL is not covered, as with one connection: after the lock is released, an `ALTER` or `DROP TABLE` can run while the rows are read.

### AUTO_INCREMENT is not restored

`AUTO_INCREMENT=N` (the counter's current value) is removed from `CREATE TABLE` so that a counter moving by itself does not produce a diff. After restoring, numbering continues from "existing maximum + 1", so if rows at the end were deleted in the past, the next number differs from the original server. The same applies to partitioned tables.

### Warnings

Every warning is a `WARN` record of the log (see `--log-level` and `--log-format`) on standard error; nothing is written to standard output. The record carries a `database` attribute, and a warning about one trigger also `table` and `trigger`, one about a routine also `routine` and `type` (`PROCEDURE` or `FUNCTION`), and one about an event also `event`. For example, in the text format: `time=... level=WARN msg="table `m` uses the MyISAM engine; ..." database=shop`. The warnings are the same between runs, in the same order; the time of a record (and the `duration` of the `INFO` records below) is what differs.

A `--password-file` that group or others can read (any of the permission bits `0044`) also gets a warning, whose message names the file and its mode (never the contents); the run goes on. Restrict the file, for example with `chmod 600`. The mode is not checked on Windows, where it carries no access rules. The warning is a plain `WARN` record without a `database` attribute, written before the connection is made.

### How the snapshot is made

The `INFO` records say how the consistent snapshot was made; `--log-level debug` adds the statements. None of them carries the password or a connection string, and none changes the output files.

| Message | Attributes | When |
| --- | --- | --- |
| `connected` | `host` and `port`, or `socket`; `jobs`; `connections` (with `--jobs` above 1: one more than `jobs`, for the lock) | after the connections are open |
| `global read lock acquired` | `duration`: how long `FLUSH TABLES WITH READ LOCK` waited | only when the lock is taken (a non-InnoDB table, or `--jobs` above 1) |
| `consistent snapshot started` | `jobs`; `global_lock`: `true` when the lock is kept until the end of the run | after `START TRANSACTION WITH CONSISTENT SNAPSHOT` |
| `global read lock released` | `duration`: how long the lock was held | right after the snapshots have started (all tables InnoDB, `--jobs` above 1), or when the run ends |

With one connection and only InnoDB tables, or with `--no-lock`, no lock is taken, so only `connected` and `consistent snapshot started` (`global_lock=false`) appear.

With `--no-lock`, tables that are not InnoDB (MyISAM and so on) cannot guarantee snapshot consistency, so a warning is printed and the dump continues. A table without a primary key or NOT NULL unique key whose estimated row count (`information_schema.TABLES.TABLE_ROWS`) exceeds 500,000 gets a warning that it is far beyond the intended scale and that its rows will be sorted through temporary files, which is slow. A table with a key gets none: its rows are streamed.

### Progress

Besides the snapshot records, each database and each table is reported as `INFO` records (hidden by `--log-level warn`). A record never carries a row value, the password or a connection string, and none changes the output files.

| Message | Attributes | When |
| --- | --- | --- |
| `dumping database` | `database` | before the first table of the database is read |
| `table dumped` | `database`, `table`, `rows`, `bytes`, `files`, `duration` | when the data files of the table are written |
| `database dumped` | `database`, `tables`, `rows`, `bytes`, `files`, `duration` | when every file of the database is written (not on a failed run) |

`rows` is the number of rows written, `bytes` and `files` are those of the data files of the table (or, in the database record, of all its tables; the schema, routines, triggers, events and constraints files are not counted), and `duration` is the time from the start of the table (or database) to its end (the time of a database also covers reading its schema, routines, triggers and events, and the tables overlap with `--jobs` above 1, so it is not the sum of theirs). With `--jobs` above 1 the tables are written at the same time, so the `table dumped` records come in the order the tables finish, not in table order. The files are written under temporary names first and renamed into place after the last database, so `table dumped` and `database dumped` do not yet mean the files are in place: a run can still fail after them, and then nothing is replaced (see "Failure handling").

## Re-dumping and removing stale files

Re-dumping into the same output directory replaces **each dumped database's files** (`<db>.00-schema.sql`, `<db>.10-data.*.sql`, `<db>.15-routines.sql`, `<db>.20-triggers.sql`, `<db>.25-events.sql` and, for `--target postgres`, `<db>.90-constraints.sql`) once every database has been written, and deletes the files of that database from last time that were not written again. Data files of tables that existed last time but are gone now therefore do not remain. Files of other databases and unrelated files such as `README.txt` are left alone. New files are always created fresh under temporary names (existing paths are never followed).

A table hidden from the dump account by missing privileges is indistinguishable from a dropped table. If a table that was visible last time becomes invisible after a privilege change, its data file is deleted as stale and is not part of the new output. Always dump with an account that has the same privileges.

### Failure handling

A run has two phases. Databases are processed one at a time, in the order given, and every file is written under a temporary name (`.mysqldance-NNNN.tmp` in the output directory; not `*.sql`, so `docker-entrypoint-initdb.d` ignores it). The sort of a table without a key also uses temporary files there (`.mysqldance-sort-NNNN.tmp`); they are never renamed into place, and a failed run removes them like the others. Only when every database has been written does the run **commit**: it renames the temporary files into place (each rename replaces the old file atomically) and deletes the files from last time that were not written again.

- **Failure before anything is read** (a bad connection or TLS setting, a database that does not exist, a system database; any one of several databases): the output directory is not touched at all. It is not created if missing, and leftover temporary files from an earlier killed run are not swept.
- **Failure before the commit** (a case-only collision, an entry with one of the database's file names that is not a regular file such as a symlink or directory, a read error, a lost connection, a write error, Ctrl-C (`SIGINT`), `SIGTERM` as sent by `docker stop` and `kill`, and so on): nothing at a final path changes, whichever database fails. The previous dump stays complete, and the temporary files are removed.
- **Failure during the commit** (a rename or delete error): the output directory can hold a mix of new and old files. File systems have no atomic update of several files, so this window cannot be closed; it is only as long as the renames take. Re-run the dump, or, if the output directory is under Git, restore the previous state from Git.
- **A second signal** (a second Ctrl-C or `SIGTERM` while the run is cleaning up) is not caught: it terminates the process at once, as `SIGKILL` would, so the next run sweeps any leftover temporary files.
- **Killed processes** (`SIGKILL`, power loss): the process cannot clean up. Temporary files may remain, and the next run deletes them before it starts. Only if the kill lands in the commit window is the output a mix as described above.

In all cases the exit code is 1. On exit, the snapshot transaction is rolled back with a 5-second limit; if the server does not answer in time, the connection is closed instead, which ends the transaction on the server.

With `--jobs` above 1 this holds for every connection: on any failure and on Ctrl-C or `SIGTERM`, all reading connections and the one holding the lock are closed, so no transaction and no global read lock is left on the server. The running tables are told to stop, and no new table is started after the first failure. When several tables fail, the error reported is that of one of the failing tables (which one can depend on timing, since the first failure stops the others), never the cancel that stopped them.

Because the old files stay until the commit, the output directory needs room for the old and the new dump at the same time. Temporary files are synced to disk before the renames, so a crash does not leave empty files in place of the previous dump. Do not run two dumps into the same output directory at once.

## Security

A dump contains the table data itself, which may include personal information or credentials. As with `mysqldump > file`, files are created with permissions that follow your umask (directories are 0755), so protect the output location with the umask, directory permissions, encrypted storage and so on. `--file-mode` and `--dir-mode` set the permissions explicitly (for example `--file-mode 0600 --dir-mode 0700`); the defaults are unchanged so that the output stays readable for `docker-entrypoint-initdb.d`. Before committing to a shared Git repository, confirm the data is fine to publish. Note too that overly restrictive permissions can make the files unreadable to the `mysql` user inside the container when used as `docker-entrypoint-initdb.d`.

## Limitations (out of scope for the MVP)

- Source servers other than MySQL 5.6 and later and MariaDB (MySQL 5.5 and earlier; see [Compatibility](#compatibility)), and output for DBMSs other than MySQL, SQLite and PostgreSQL. Parallel reading is per table (`--jobs`); one table is never read on several connections.
- Tables without a key are sorted, not streamed. A table with a primary key or a NOT NULL unique key is read and written row by row, so its memory use does not grow with its size (a table of one million rows needs a few megabytes). A table without such a key is sorted by the byte order of each row's output string with an external sort: the rows are read into memory up to `--sort-memory` (64MiB by default), each full batch is sorted and written to a temporary file, and the batches are merged to write the table, so memory does not grow with the table either (a table of one million rows needs about the limit plus a few megabytes). The temporary files are in the output directory, named `.mysqldance-sort-NNNN.tmp`, with the same permission as the output files (`--file-mode`), and each is deleted as soon as it has been merged; they need disk space of about the size of the table's rows as text, plus a second copy while a very large table (over 64 batches) is merged in passes. The output is the same, byte for byte, as when the rows are sorted in memory. All the rows are still read before the first is written, and sorting takes time, so a table without a key is slower than one with a key; with `--jobs N`, N tables are sorted at once.
- Users and privileges. (Triggers, stored routines and events are dumped for `--target mysql` only.)
- **No order between databases.** Views are ordered by their dependencies within one database only. A view that reads a table or a view of another database (`CREATE VIEW v AS SELECT ... FROM other.t`) restores only if `other` exists when its `CREATE VIEW` runs, and the files of several databases run in file name order, so `CREATE VIEW` fails with `Unknown database` (or `Table ... doesn't exist`) when `other`'s files sort after this database's. Checked on MySQL 8.0 and 8.4. The dump does not reorder anything: it logs a `WARN` for each such view (`view `v` reads database `other`: it must be restored before this one ...`), whatever the order of the files, since it cannot know where the files are restored. Restore the other database first, by loading its files by hand before the rest (the file name order follows the database names, and the dump order of the databases does not change it). The system schemas (`mysql`, `sys`, `information_schema`, `performance_schema`) are not reported. On servers without `VIEW_TABLE_USAGE` (MariaDB, MySQL 5.7 and 5.6) the databases are found by a text search of the view definition, so a table alias that has the name of another database also gets the warning.
- Flags must be placed before the database names.

## Development

```sh
task test                                   # unit tests (no MySQL needed; integration tests are skipped)
task test:integration                       # start MySQL 8.0 and 8.4 in Docker and run integration tests
task test:integration:mariadb               # the same against MariaDB 10.11 and 11.4 (not part of the line above)
task test:integration:mysql57               # the same against MySQL 5.7.44 (amd64 image, so slow to start on arm64; not part of the line above)
task test:integration:mysql56               # the same against MySQL 5.6.51 (amd64 image; restores into 8.4, then MariaDB 10.11; not part of the line above)
task test:integration HOST=host.docker.internal TESTFLAGS=-v   # when the Docker daemon is on another host
task lint                                   # golangci-lint
```

Integration tests run only when the `MYSQLDANCE_TEST_DSN` environment variable is set (for example `root:root@tcp(127.0.0.1:33061)/`).
CI (`.github/workflows/integration.yml`) runs them against MySQL 8.0 and 8.4 on every push and fails the job if they are skipped.

The `fix` and `update-deps` workflows are run by hand (`workflow_dispatch`). Each runs `go fix` or `go get -u`, then the unit tests, and opens a PR only if they pass. A PR created with the default `GITHUB_TOKEN` does not trigger the `branch` workflow, so CI does not start by itself. Before merging, run `branch` on the PR's branch (`chore/go-fix` or `chore/update-deps`) from the Actions tab, or with `gh workflow run branch.yml --ref chore/update-deps`, to run the integration tests as well.

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
