# mysqldance MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** MySQL に直接接続し、1 レコード 1 行・決定的な出力のダンプをフラットなディレクトリに書き出す Go 製 CLI `mysqldance` を作る（MVP）。

**Architecture:** 純粋関数のパッケージ（名前エスケープ、値のリテラル化、スキーマ変換、データファイル書き出し）を先に作り、MySQL 接続層（`internal/source`）とそれらを束ねる `internal/dump`、CLI を後から載せる。実 MySQL が要るテストは環境変数がなければ skip する統合テストに分ける。

**Tech Stack:** Go 1.22+、`github.com/go-sql-driver/mysql`、標準 `flag`、`task`（go-task）、`gofmt` / `golangci-lint`、MySQL 8.0 / 8.4（Docker）。

**Spec:** `docs/superpowers/specs/2026-11-09-mysqldance-design.md`（以降「設計書」。節番号は設計書のもの）

## Global Constraints

- 言語は Go、モジュール名 `mysqldance`。ソースコードのコメントとログは英語、ドキュメントは日本語。
- 対象は MySQL 8.0 系以降のみ（MariaDB・5.7 は対象外）。バージョン依存の処理は `internal/source` の 1 か所にまとめる。
- 出力は 1 つのフラットなディレクトリ。ファイル名は `<db>.00-schema.sql` と `<db>.10-data.<table>.sql`。使ってよい文字は ASCII 英数字と `_` `-`（区切りの `.` を除く）。他はエスケープする。
- データファイルは `INSERT INTO \`t\` VALUES` の後に 1 レコード 1 行（extended-insert）。同じデータなら同じバイト列。
- 固定ヘッダー（全 SQL ファイル共通。スキーマファイルは `USE` の前に `CREATE DATABASE` 行が入る）:
  ```
  SET NAMES utf8mb4;
  SET TIME_ZONE='+00:00';
  SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';
  SET FOREIGN_KEY_CHECKS=0;
  SET UNIQUE_CHECKS=0;
  USE `<db>`;
  ```
- ダンプ接続も `time_zone='+00:00'`、`utf8mb4`、`REPEATABLE READ` + `START TRANSACTION WITH CONSISTENT SNAPSHOT`。
- 行順: 主キー → NOT NULL ユニークキー（インデックス名順で最初の 1 つ）→ キーなしは各行の出力文字列をバイト昇順ソート。文字列キーはバイナリ比較。
- ビューの `DEFINER=...` 句は除く。`CREATE TABLE` の ` AUTO_INCREMENT=N` は除く。スキーマファイルは `DROP TABLE/VIEW IF EXISTS` を `CREATE` の前に置き、テーブル→ビュー（ビュー間は依存順、同順位は名前順）。
- BLOB / BINARY / GEOMETRY は常に `0x` + hex（空は `''`）、BIT は `b'...'`、NULL は `NULL`。
- 生成列は出力しない。生成列または `INVISIBLE` 列を持つテーブルだけ `INSERT INTO \`t\` (\`c1\`, ...) VALUES`。
- システム DB（`information_schema` `mysql` `sys` `performance_schema`）の指定はエラー。非 InnoDB は警告して続行。
- MVP 対象外: 並列取得、他 DBMS、範囲分割、トリガー / ルーチン / イベント（設計書 12 節）。

## Review Focus

設計書が暗に含むが、各タスクのテストだけでは拾いにくい入力。各行のテストは担当タスクに入れてある。

1. 文字列の `\` `'` `"` 改行 `NUL`、不正な UTF-8、巨大 BLOB が、ダンプ→リストアで完全に一致する（Task 2, 6）。
2. 0 行のテーブル: `INSERT` 文を出さず、ヘッダーだけのファイルになる（Task 3, 5）。
3. 再ダンプ時、前回あったテーブルが消えていたら古いデータファイルが残らない（Task 6）。
4. 英大文字・空白・バッククォート・非 ASCII・`.` を含む名前、大文字小文字だけが違うテーブル名の衝突（Task 1, 6）。
5. キーなしで完全に重複する行、NULL 可ユニークキーのみのテーブル、文字列主キーで `a` `A` `B` の並び（Task 5）。

---

## File Structure

```
go.mod
Taskfile.yml
.golangci.yml
README.md
cmd/mysqldance/main.go            # flags -> dump.Run
internal/naming/naming.go         # filename escaping, file name builders
internal/sqlvalue/sqlvalue.go     # column value -> SQL literal
internal/datafile/datafile.go     # header + chunked one-row-per-line INSERT writer
internal/schemafile/schemafile.go # DDL transforms, ordering, schema file writer
internal/source/source.go         # MySQL access: snapshot, introspection, ordered rows
internal/source/itest_test.go     # integration test helpers (skip without env)
internal/dump/dump.go             # orchestration, stale file cleanup, warnings
internal/dump/dump_test.go
```

---

### Task 1: プロジェクトの土台とファイル名規則

**Files:**
- Create: `go.mod`（`module mysqldance`）、`Taskfile.yml`（`test`、`lint`、`fmt`、`test:integration`）、`.golangci.yml`
- Create: `internal/naming/naming.go`
- Test: `internal/naming/naming_test.go`

**Interfaces:**
- Produces:
  - `func EscapeName(s string) string` — `[A-Za-z0-9_-]` はそのまま、それ以外（`.` と `%` を含む）は UTF-8 の各バイトを `%XX`（大文字 hex）にする。
  - `func SchemaFile(db string) string` → `<esc(db)>.00-schema.sql`
  - `func DataFile(db, table string) string` → `<esc(db)>.10-data.<esc(table)>.sql`
  - `func IsOwnedBy(file, db string) bool` — `file` が `SchemaFile(db)` または `<esc(db)>.10-data.` で始まり `.sql` で終わるか（再ダンプ時の古いファイル削除に使う）。

- [ ] **Step 1: テストを書く**（`naming_test.go`）。`EscapeName("a.b")=="a%2Eb"`、`EscapeName("日")=="%E6%97%A5"`、`EscapeName("100%")=="100%25"`、`EscapeName("a_b-c")=="a_b-c"`。`DataFile("my db","T")=="my%20db.10-data.T.sql"`。`IsOwnedBy("a.10-data.t.sql","a")==true`、`IsOwnedBy("a-b.10-data.t.sql","a")==false`、`IsOwnedBy("a.00-schema.sql","a")==true`。
- [ ] **Step 2: 辞書順のテスト**: DB 名 `a`, `a-b`, `a_b`, `a.b` で、各 DB について `SchemaFile` がその DB の全 `DataFile` より文字列比較で小さい。
- [ ] **Step 3: 失敗を確認** — `go test ./internal/naming/`（`go.mod` 作成後）。FAIL（未定義）。
- [ ] **Step 4: 実装**（上記シグネチャ）。`Taskfile.yml` と `.golangci.yml` も同時に作る。
- [ ] **Step 5: 成功を確認** — `go test ./internal/naming/` PASS、`gofmt -l .` が空、`golangci-lint run` が通る。
- [ ] **Step 6: コミット** — `git add go.mod Taskfile.yml .golangci.yml internal/naming && git commit -m "feat: add project skeleton and file name escaping"`

---

### Task 2: 値の SQL リテラル化

**Files:**
- Create: `internal/sqlvalue/sqlvalue.go`
- Test: `internal/sqlvalue/sqlvalue_test.go`

**Interfaces:**
- Produces:
  - `type Column struct { Name string; TypeName string }` — `TypeName` は `database/sql` の `ColumnType.DatabaseTypeName()`（`INT`, `VARCHAR`, `BLOB`, `BIT`, `DECIMAL` など）。
  - `func Literal(typeName string, raw []byte, isNull bool) string`
- 規則: NULL → `NULL`。数値型（`TINYINT` `SMALLINT` `MEDIUMINT` `INT` `BIGINT` `UNSIGNED ...` 各種、`DECIMAL` `FLOAT` `DOUBLE` `YEAR`）はサーバが返したテキストをそのまま。`BIT` は `b'<2進>'`。`BLOB` 系・`BINARY` `VARBINARY` `GEOMETRY` は `0x<HEX 大文字>`、空は `''`。それ以外は文字列として `'...'` で、`\0`→`\0`、`\n`→`\n`、`\r`→`\r`、`\\`、`\'`、`"`→`\"`、`0x1A`→`\Z` をエスケープ（mysql_real_escape_string 相当）。

- [ ] **Step 1: テストを書く**（表駆動）: `Literal("INT",[]byte("42"),false)=="42"`、`Literal("VARCHAR",nil,true)=="NULL"`、`Literal("VARCHAR",[]byte("a'b\\c\n\r\"\x00\x1a"),false)==`"'a\\'b\\\\c\\n\\r\\\"\\0\\Z'"``、`Literal("BLOB",[]byte{0xDE,0xAD},false)=="0xDEAD"`、`Literal("BLOB",[]byte{},false)=="''"`、`Literal("BIT",[]byte{0x05},false)=="b'101'"`、`Literal("DECIMAL",[]byte("1.50"),false)=="1.50"`、不正な UTF-8 `[]byte{0xFF,0xFE}` を `VARCHAR` に渡しても panic せず、バイトをそのままエスケープして返す。
- [ ] **Step 2: 失敗を確認** — `go test ./internal/sqlvalue/`。FAIL。
- [ ] **Step 3: 実装** — `Literal`。`BIT` は先頭ゼロを除くが、全ゼロは `b'0'`。
- [ ] **Step 4: 成功を確認** — PASS。
- [ ] **Step 5: コミット** — `feat: add SQL literal formatting`

---

### Task 3: データファイルの書き出し（チャンク分割）

**Files:**
- Create: `internal/datafile/datafile.go`
- Test: `internal/datafile/datafile_test.go`

**Interfaces:**
- Produces:
  - `type Options struct { RowsPerInsert uint32; MaxInsertBytes int }`（既定 1000 / 1048576）
  - `func Header(db string) string` — Global Constraints の固定ヘッダー（末尾に改行）。
  - `func Write(w io.Writer, db, table string, columns []string, listColumns bool, rows [][]string, opt Options) error` — `rows` は各行がリテラル化済みの値の列。`listColumns` が true のとき `INSERT INTO \`t\` (\`a\`, \`b\`) VALUES`。0 行ならヘッダーのみ。

**チャンクの決め方（設計書 4.2 の「決定的な規則」を、この計画で具体化した判断）**: 固定件数で区切ると、途中で 1 行消えただけで以降のすべての `INSERT` 境界がずれ、差分があちこちに出る。そこで「行の出力文字列の FNV-1a 32bit ハッシュ % `RowsPerInsert` == 0 の行で文を閉じる」内容依存の区切りにする。加えて文のバイト数が `MaxInsertBytes` を超えたらその行で閉じる（上限は安全弁）。

- [ ] **Step 1: テストを書く**:
  - `TestWriteOneRowPerLine`: 3 行 → 出力が `INSERT INTO \`t\` VALUES\n(1,'a'),\n(2,'b'),\n(3,'c');\n`（ヘッダーの後）。
  - `TestWriteEmptyTableWritesHeaderOnly`: 0 行 → `Header(db)` と完全一致。
  - `TestWriteListColumns`: `listColumns=true` で `INSERT INTO \`t\` (\`a\`, \`b\`) VALUES`。
  - `TestChunkBoundariesAreLocal`: 5000 行を書き、途中の 1 行を削除した版を書いて行単位で diff を取ると、変更行は「削除した行」＋「文末記号 `,`/`;` と `INSERT` 行が関わる高々数行」だけ（削除行を含め 6 行以内）。
  - `TestMaxInsertBytes`: `MaxInsertBytes` を小さくすると、すべての `INSERT` 文のバイト数が（1 行分の余裕を除き）上限以内。
  - `TestDeterministic`: 同じ入力 2 回で同一バイト列。
- [ ] **Step 2: 失敗を確認** — `go test ./internal/datafile/`。FAIL。
- [ ] **Step 3: 実装** — `Header`、`Write`（`bufio.Writer` で書く）。
- [ ] **Step 4: 成功を確認** — PASS。
- [ ] **Step 5: コミット** — `feat: add chunked one-row-per-line data file writer`

---

### Task 4: スキーマ変換とスキーマファイル

**Files:**
- Create: `internal/schemafile/schemafile.go`
- Test: `internal/schemafile/schemafile_test.go`

**Interfaces:**
- Produces:
  - `func StripAutoIncrement(createTable string) string` — テーブルオプションの ` AUTO_INCREMENT=<数字>` を除く（列定義の `AUTO_INCREMENT` キーワードは残す）。
  - `func StripDefiner(createView string) string` — `` DEFINER=`u`@`h` `` と後続の空白 1 つを除く。
  - `func OrderViews(views []string, deps map[string][]string) []string` — `deps[v]` は `v` が参照する同一 DB 内のビュー名。依存先が先、同順位は名前の昇順（カーン法）。
  - `type Table struct { Name, Create string }`、`type View struct { Name, Create string }`
  - `func Write(w io.Writer, db, createDatabase string, tables []Table, views []View) error` — `createDatabase` は `SHOW CREATE DATABASE` の結果（`IF NOT EXISTS` を挿入して使う）。出力順: `CREATE DATABASE` 行 → 固定ヘッダー（`USE` 含む）→ テーブル（名前順、各 `DROP TABLE IF EXISTS` + `CREATE`）→ ビュー（渡された順。順序づけは呼び出し側の `OrderViews`）。

- [ ] **Step 1: テストを書く**: `StripAutoIncrement("... ) ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4")` が ` AUTO_INCREMENT=42` だけ除く。列定義 ``` `id` int NOT NULL AUTO_INCREMENT``` は変わらない。`StripDefiner("CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1")` が `CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW ...`。`OrderViews`: `c` が `a` に、`a` が `b` に依存、`x` は独立 → `[b a c x]`、同順位の別名順入力でも結果が同一。`Write` の出力の先頭が `CREATE DATABASE IF NOT EXISTS` で、テーブルがビューより前、テーブルが名前順。
- [ ] **Step 2: 失敗を確認** — FAIL。
- [ ] **Step 3: 実装**（上記シグネチャ）。
- [ ] **Step 4: 成功を確認** — PASS。
- [ ] **Step 5: コミット** — `feat: add schema transforms and schema file writer`

---

### Task 5: MySQL 読み出し層（統合テスト）

**Files:**
- Create: `internal/source/source.go`、`internal/source/itest_test.go`
- Test: `internal/source/source_test.go`

**テスト環境**: 環境変数 `MYSQLDANCE_TEST_DSN`（例 `root:pw@tcp(127.0.0.1:3306)/`）が空なら `t.Skip`。各テストは一意な名前の DB を作り、`t.Cleanup` で `DROP DATABASE`。`Taskfile.yml` の `test:integration` が Docker で `mysql:8.0` と `mysql:8.4` を順に起動して実行する。

**Interfaces:**
- Consumes: `sqlvalue.Literal`（Task 2）
- Produces:
  - `type Config struct { Host string; Port int; Socket, User, Password string }`
  - `func Open(ctx context.Context, cfg Config, db string) (*Snapshot, error)` — 1 接続で `time_zone`・`utf8mb4`・`REPEATABLE READ`・`START TRANSACTION WITH CONSISTENT SNAPSHOT` を設定。システム DB 名ならエラー。
  - `func (s *Snapshot) Close() error`
  - `func (s *Snapshot) CreateDatabase(ctx) (string, error)` — `SHOW CREATE DATABASE`
  - `type TableInfo struct { Name, Create, Engine string; Columns []sqlvalue.Column; ListColumns bool; OrderBy []string; ApproxRows int64 }` — `Columns` は生成列を除く。`ListColumns` は生成列 / `INVISIBLE` 列を持つとき true。`OrderBy` は主キー、なければ NOT NULL ユニークキー（インデックス名順で最初）、なければ空。
  - `func (s *Snapshot) Tables(ctx) ([]TableInfo, error)`、`func (s *Snapshot) Views(ctx) (views []schemafile.View, deps map[string][]string, err error)`
  - `func (s *Snapshot) Rows(ctx context.Context, t TableInfo) ([][]string, error)` — リテラル化済みの行。`OrderBy` があれば `ORDER BY` はキー列を `BINARY`（文字列型のみ）で並べる。空ならサーバ側の順は使わず、各行を `,` で連結した文字列のバイト昇順に Go 側でソート。SELECT は `Columns` を明示列挙（`SELECT *` は使わない）。
  - `func (s *Snapshot) Warnings() []string` — 非 InnoDB のテーブルがある旨、キーなしで `ApproxRows > 500000` の旨。

- [ ] **Step 1: 失敗するテストを書く**（上記 `skip` 付き）:
  - 主キー付き: 行がキー順で返る。文字列主キー `a`,`B`,`A` が `A`,`B`,`a`（バイナリ順）。
  - NOT NULL ユニークキーのみ / NULL 可ユニークキーのみ（後者は OrderBy が空）。
  - キーなしで完全に重複する行が 2 つ → 両方出力され、挿入順を変えても同じ並びになる。
  - 生成列と `INVISIBLE` 列を持つテーブル: `Columns` に生成列が含まれず、`ListColumns==true`、`INVISIBLE` 列の値は出力される。
  - 0 行のテーブル: `Rows` が空。
  - 値の型: `BLOB`（NUL・`0xFF`）、`BIT(5)`、`DATETIME`、`TIMESTAMP`（接続の time_zone に依存しない）、`JSON`、`DECIMAL`、`FLOAT`、`TEXT`（改行・クォート）が期待リテラルになる。
  - システム DB 名（`mysql`）で `Open` がエラー。
  - ビュー: `VIEW_TABLE_USAGE` から `deps` が得られる（ビュー `v2` が `v1` を参照）。
  - 非 InnoDB（MyISAM）のテーブルで `Warnings()` に含まれる。
- [ ] **Step 2: 失敗を確認** — `MYSQLDANCE_TEST_DSN=... go test ./internal/source/`。FAIL。
- [ ] **Step 3: 実装** — `information_schema.COLUMNS`（`EXTRA` に `GENERATED` / `INVISIBLE`）、`STATISTICS`（キー選択）、`TABLES.ENGINE` / `TABLE_ROWS`、`VIEW_TABLE_USAGE`、`SHOW CREATE TABLE/VIEW/DATABASE`。`Rows` は `sql.RawBytes` で受けてテキストプロトコルのまま `sqlvalue.Literal` に渡す。バージョン依存のクエリはこのパッケージ内の 1 か所に集める。
- [ ] **Step 4: 成功を確認** — 8.0 と 8.4 の両方で PASS（`task test:integration`）。DSN なしでは skip され `go test ./...` は通る。
- [ ] **Step 5: コミット** — `feat: add MySQL snapshot reader`

---

### Task 6: ダンプの組み立てと CLI

**Files:**
- Create: `internal/dump/dump.go`、`cmd/mysqldance/main.go`、`README.md`
- Test: `internal/dump/dump_test.go`

**Interfaces:**
- Consumes: `naming`（Task 1）、`datafile`（Task 3）、`schemafile`（Task 4）、`source`（Task 5）
- Produces:
  - `type Options struct { Source source.Config; DBs []string; OutDir string; Data datafile.Options; Log io.Writer }`
  - `func Run(ctx context.Context, opt Options) error`
- 振る舞い: 出力ディレクトリがなければ作る。DB ごとに、まず `naming.IsOwnedBy` に一致する既存ファイルだけを削除（他のファイルは触らない）→ スキーマ → 各テーブルのデータ。出力ファイル名が小文字化して衝突する場合（`Users` と `users`）はエラー。`Warnings()` は `Log` に出す。
- CLI フラグ: `-h/--host`（既定 127.0.0.1）、`-P/--port`（3306）、`-u/--user`、`--socket`、パスワードは環境変数 `MYSQL_PWD` か `--password-file`（コマンドラインに直接は受けない）、`-o/--output`（必須）、`--rows-per-insert`（1000）、`--max-insert-bytes`（1048576）、位置引数に DB 名（1 つ以上）。終了コード: 成功 0、エラー 1。

- [ ] **Step 1: 失敗するテストを書く**（統合、DSN なしで skip）:
  - `TestRoundTrip`: 複数テーブル（外部キー循環、キーなし、`BLOB`、改行入り文字列、ビュー同士の依存）を持つ DB をダンプし、別名 DB に（ファイル名の辞書順で `mysql` クライアント相当に流して）リストアし、全テーブルの内容が `CHECKSUM` ではなく全行比較で一致する。
  - `TestDeterministicAndMinimalDiff`: 同じ DB を 2 回ダンプして全ファイルがバイト一致。1 レコード更新して再ダンプすると、`git diff --no-index` 相当の行差分が「その 1 行（削除 1 + 追加 1）」だけ。
  - `TestStaleFilesRemoved`: テーブルを 1 つ `DROP` して同じ出力先に再ダンプすると、そのテーブルのデータファイルが消え、無関係なファイル（`README.txt`）は残る。
  - `TestOddNamesAndCaseCollision`: 空白・バッククォート・非 ASCII を含むテーブル名がダンプ・リストアできる。`lower_case_table_names=0` 環境で `Users` と `users` を作ると `Run` がエラー（小文字化衝突）。
  - `TestMultipleDatabases`: 2 つの DB を 1 ディレクトリへ。各 DB について辞書順でスキーマがデータより先。
  - CLI の最小テスト（`go test ./cmd/...`、DB 不要）: `-o` なし、DB 名なしで終了コード 1。
- [ ] **Step 2: 失敗を確認** — FAIL。
- [ ] **Step 3: 実装** — `Run`、`main.go`。`README.md` に使い方（`docker run -v ./dump:/docker-entrypoint-initdb.d mysql`）、`AUTO_INCREMENT` を復元しない旨、非 InnoDB の警告、MVP 対象外を書く。
- [ ] **Step 4: 成功を確認** — `task test`（統合なし）と `task test:integration`（8.0 / 8.4）が PASS、`golangci-lint run` 通過。
- [ ] **Step 5: コミット** — `feat: add dump orchestration and CLI`

---

### Task 7: 未確認事項の実機確認と設計書への反映

**Files:**
- Modify: `docs/superpowers/specs/2026-11-09-mysqldance-design.md`（11 節）

- [ ] **Step 1: 確認する** — (a) 公式 `mysql:8.0` / `8.4` イメージに Task 6 の出力ディレクトリを `/docker-entrypoint-initdb.d` としてマウントして起動し、データが復元されること、サブディレクトリが辿られないこと。(b) `BIT` `BLOB` の mysqldump の実際の表記と本ツールの出力の違い（`mysqldump --hex-blob` と比較）。(c) `VIEW_TABLE_USAGE` が 8.0.13 以降で使えること（8.0 / 8.4 の最新タグで確認）。
- [ ] **Step 2: 11 節を更新** — 確認済みの項目は結果を書き、差異があれば本文の該当箇所も直す。
- [ ] **Step 3: コミット** — `docs: record verified assumptions`

---

## Self-Review メモ

- 設計書の各節: 4.1 → Task 1, 6 / 4.2 → Task 2, 3 / 4.3 → Task 4 / 5 → Task 3, 5 / 6 → Task 5 / 7 → Task 6 / 8（構造）→ Task 5, 3 のパッケージ分割 / 9 → 各タスクのテスト / 10.5 → Global Constraints / 11 → Task 7。
- 12 節（バックログ）は対象外。
