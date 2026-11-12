# mysqldance 設計書

日付: 2026-11-09
状態: 承認済み（実装済み: MVP）。2026-11-09 にユーザが承認した。

凡例: 【決定】= ユーザが決めたこと / 【判断】= Claudeの判断（要確認） / 【要確認】= 実装時に実機で確かめること

## 1. 背景と目的

`mysqldump` の extended-insert 出力は、1つの INSERT 文が1行に数万〜数百万文字で並ぶ。1レコード変更しただけでも長大な行全体が差分になり、`git diff` で変更箇所を目で追えない。
`--skip-extended-insert` なら1レコード1行になり差分は綺麗だが、リストアが著しく遅く実用にならない。

**目的**: リストア速度（extended-insert 相当）を保ったまま、1レコード1行で出力し、`git diff` で変更が見えるダンプツール `mysqldance` を作る。

【決定】最終的なビジョンは「mysqldump の代替となる高機能ダンプツール」。ただし MVP は絞る（後述）。
- 最終的に欲しい特性: 高速（並列取得）、バックアップファイルをgit管理して差分が追える、他DBMS（PostgreSQL / SQLite）へデータを移せる。

【決定】mysqldump の出力をフィルタする方式ではなく、MySQL に直接接続する自前ダンパーとして作る。理由は、並列取得・他DBMS出力といった将来拡張をフィルタ方式では載せられないため。
- 整形だけなら簡単だが、自前ダンパーは重い。MVP でその重さを引き受ける範囲は「テーブル＋ビューの出力」に限る。

## 2. ゴール / 非ゴール / アンチゴール

### MVP のゴール
- 小さな DB をダンプし、バックアップファイルを git 管理したとき、`git diff` でレコード単位の変更が綺麗に見える。
- 出力はそのまま `mysql < file.sql` や MySQL 公式 Docker イメージの `/docker-entrypoint-initdb.d/` でリストアでき、速度は mysqldump の extended-insert と同等。

### MVP の非ゴール（将来拡張。構造上の余地だけ残す）
- 並列取得
- PostgreSQL / SQLite 向け出力
- 巨大テーブルの主キー範囲による分割出力
- トリガー、ストアドルーチン、イベント
- 日常運用のバックアップ置き換え（MVPの成功基準は「小さなDBで git diff が綺麗に出る」こと）
- 「前回からの差分だけを取る」増分バックアップ。【決定】差分バックアップとは、出力が決定的で git のテキスト差分・delta が効くことを指す。履歴整理は git 側（shallow clone 等）の仕事でツールは関与しない。

### アンチゴール（技術的に動いても失敗とみなす）
- 同じデータなのに実行のたびに出力が変わり、git に無駄な差分が出る。
- リストアが `--skip-extended-insert` 並みに遅くなる。
- 出力を読み間違えて、データが破損・欠落する（値のエスケープ、型の扱いの誤り）。

## 3. 技術選定

【決定】言語は Go。単一バイナリで配布でき、並列処理が書きやすい。リンタ・ビルドは `gofmt` / `golangci-lint` / `goreleaser` / `go test`、タスクランナーは `task`（ユーザのグローバル開発ルールに従う）。
- MySQL ドライバ、CLI ライブラリ等の具体選定は実装者に任せる。

## 4. 出力仕様

### 4.1 ファイルレイアウト

【決定】出力は1つのフラットなディレクトリ。サブディレクトリは使わない（MySQL 公式イメージの `docker-entrypoint-initdb.d` はサブディレクトリを辿らないため。11 節で確認済み）。ファイル名の辞書順にロードすれば正しく復元できること。複数 DB を同じディレクトリに入れられること。

```
dump/
  mydb.00-schema.sql        # 固定ヘッダーの SET 群 / CREATE DATABASE IF NOT EXISTS / USE / テーブル・ビュー定義
  mydb.10-data.users.sql    # 固定ヘッダー（SET 群 + USE mydb）+ INSERT
  mydb.10-data.orders.sql
```

- 辞書順で「スキーマ → データ」の順になる。
- 各ファイルに `USE <db>` を含めるので単独でも流せ、複数 DB が混ざらない。
- 【判断】ファイル名に使うのは ASCII の英数字と `.` `_` `-` のみ。それ以外を含む DB 名・テーブル名はエスケープ規則を決めて対応する（規則は実装者が決め、README と本書に追記する。ロケールによる辞書順の変動を避けるため）。
  - 【実装】`internal/naming` の規則: DB 名・テーブル名のうち `[A-Za-z0-9_-]` のバイトはそのまま残し、それ以外（`.` `%` 空白、非 ASCII の UTF-8 の各バイトを含む）は 1 バイトずつ `%XX`（大文字の 16 進）にする。例: `my.db` → `my%2Edb`、`日本` → `%E6%97%A5%E6%9C%AC`。区切りの `.` は名前の中に現れないので、`mydb` と `mydb2` のように接頭辞を共有しても並びが崩れない。エスケープ後のファイル名が 255 バイトを超えたらエラー（その DB のファイルは何も書かない）。小文字にすると同じになるファイル名が 1 回の実行の中にあってもエラー。
- 【判断】将来の範囲分割は `mydb.10-data.orders.0001.sql` のように末尾に番号を足す。MVP では作らない。
  - 【実装】`--rows-per-file N`（B-009、既定 0 = 分割しない）で作る。主キー（なければ並べ替えのキー、それも無ければ行全体）のハッシュ（FNV-1a を混ぜたもの）を N で割った余りが 0 の行の後でファイルを閉じる。分割して 2 ファイル以上になったテーブルだけ、`mydb.10-data.orders.0001.sql` のように 4 桁以上のゼロ詰めの番号を付ける（1 ファイルなら従来の名前）。ファイル数が 10000 以上なら、そのテーブルの全ファイルを同じ桁数にそろえる。
- 使い方: `docker run -v ./dump:/docker-entrypoint-initdb.d mysql` で初期化データとしてそのまま使える。

### 4.2 データファイル

【決定】`INSERT INTO t VALUES` の後ろに、1レコード1行で並べる。リストア速度を保つため複数行を1文にまとめる（extended-insert）。

```
INSERT INTO `t` VALUES
(1,'a'),
(2,'b'),
(3,'c');
```

- 【判断】1つの INSERT 文に含める最大レコード数・最大バイト数の上限を設ける（`max_allowed_packet` 超過回避）。上限に達したら新しい INSERT 文を始める。上限値は設定可能にし、デフォルトは実装者が決める。ただし分割位置が実行ごとに変わらないよう、決定的な規則（レコード数ベース等）にすること。
  - 【実装】内容で区切る規則（content-defined chunking）: 各行の出力文字列 `(...)` の FNV-1a 32 ビットハッシュを `RowsPerInsert` で割った余りが 0 の行の後で文を閉じる。文の大きさが `MaxInsertBytes` に達した行の後でも閉じる。最後の行の後は必ず閉じる。既定値は `RowsPerInsert` = 1000（平均の行数）、`MaxInsertBytes` = 1048576。区切りが行の内容だけで決まるので、1 行の追加・削除・更新で変わるのはその行の近くだけになる。
- 各ファイルの先頭に固定ヘッダーを付ける（`USE <db>`、`SET FOREIGN_KEY_CHECKS=0`、`SET UNIQUE_CHECKS=0`、文字コード設定など）。ヘッダーは毎回同じ内容なので差分に出ない。

### 4.3 スキーマファイル

- 【実装】ファイル内の順序: 固定ヘッダーの `SET` 群（`SET NAMES utf8mb4` が先頭）→ `CREATE DATABASE IF NOT EXISTS ...` → `USE <db>` → テーブル → ビュー。`SET NAMES` を `CREATE DATABASE` より先にするのは、クライアントの既定の文字コードが `utf8mb4` でなくても非 ASCII の DB 名を正しく読ませるため。
- `SHOW CREATE TABLE` の結果（外部キー・制約を含む）をそのまま使う。【決定】外部キーはテーブル定義内にインラインで含める。mysqldump と同じ考え方。
- テーブルは名前順に並べる（依存関係順にはしない）。ヘッダーで `FOREIGN_KEY_CHECKS=0` にするので順序は問題にならず、循環参照する外部キーも許容される。
- テーブルを先に、ビューを後に並べる。ビュー同士の依存は依存関係順に並べる必要がある。`information_schema.VIEW_TABLE_USAGE` は MySQL 8.0.13 以降で使える（8.0.46 / 8.4.13 で確認済み。11 節）。使えない環境でのフォールバックは実装者が決める（例: 定義の文字列解析、または制限事項として明記）。
- `AUTO_INCREMENT=N` の現在値は、テーブル定義に含まれると差分の原因になる。【判断】定義からは除き、データから導ける形にするか、方針を決めて README に書く。実装者が決めてよい（要レビュー）。

## 5. 決定的な出力（最重要要件）

同じデータなら必ず同じバイト列になること。

- 【判断】行の並び順:
  1. 主キーがあれば主キー順。
  2. なければ NOT NULL のユニークキー順。
  3. どちらもなければ全列の値でソートして順序を固定する。大きなテーブルで遅くなる場合は警告を出す。
- 出力に、ダンプ時刻・サーバーバージョン・ホスト名など毎回変わる行を含めない。
- 参考: mysqldump はデフォルトで `ORDER BY` を付けず、InnoDB ではクラスタ化インデックス順（主キーがなければ隠し行 ID 順＝ほぼ挿入順）で返る。`OPTIMIZE TABLE`・再構築・レプリカ・リストアし直しで順序が変わりうるため、本ツールは明示的に `ORDER BY` を使う。
- 値の表現（数値、文字列のエスケープ、NULL、日時、BLOB/BINARY は 16 進、BIT、JSON、浮動小数点の表記）は実行ごとに同一でなければならない。【判断】基本は mysqldump の表記に合わせる（BLOB / BINARY / VARBINARY は `0x` + 16 進）。ただし次の 2 点は意図的に mysqldump と異なる【差異あり。どちらも意図的な差異として残す。根拠は 11 節】。(1) `BIT` は mysqldump が 16 進（`0x15`）で出すのに対し、本ツールは `b'10101'` 形式で出す（値が読みやすく、バイナリ文字を含まない）。(2) 空の BLOB は mysqldump が `_binary ''`、本ツールは `''`（本ツールは 16 進かプレーンなリテラルだけを出し、空の値は常に `''` と書くため。復元後は同じ空の BLOB になる）。なお浮動小数点は、`DOUBLE` はテキストプロトコルの文字列のまま出す。`FLOAT` はサーバーが有効数字 6 桁で出して値が変わるので、`` `f` * 1e0 `` で正確な `DOUBLE` として読み、`float32` の最短表記に直して出す。`-0` は整数 `0` の単項マイナスになって符号が落ちるので、`-0e0` と出す【決定。B-059。検証結果は 11 節】。
- 注: `BOOLEAN` は `TINYINT(1)` の別名で、データ部分は `0`/`1` の数値。型の情報は `CREATE TABLE` 側にしか残らない。

## 6. 一貫性

【判断】MVP は1接続で `REPEATABLE READ` + `START TRANSACTION WITH CONSISTENT SNAPSHOT` を使い、全テーブルが同一時点の内容になるようにする。テーブルは順に処理する。
- 【実装】複数 DB も同じ接続・同じトランザクションで読む（`source.Snapshot.Use` で `USE` を切り替える）ので、DB をまたいでも同一時点の内容になる。切り替えるとき、DB ごとのキャッシュ（列カタログ、警告）は捨てる。
- 並列取得では、複数接続を同一スナップショットに揃える。方式は 6.1 で確定し、B-042 で実装した（`--jobs N`、既定 1）。

### 6.1 並列取得の一貫性（B-041 で実機検証済み）

【決定】並列取得は、ロック用の 1 接続と読み出し用の N 接続で構成する。手順は次のとおり。

1. ロック用の接続で `FLUSH TABLES WITH READ LOCK` を実行する。
2. 読み出し用の各接続で `SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ` と `START TRANSACTION WITH CONSISTENT SNAPSHOT` を実行する。ロックを持ったまま、全接続で行う。
3. 全接続のスナップショットが開始したら、**全テーブルが InnoDB なら `UNLOCK TABLES` で直ちに解放する**。**InnoDB 以外のテーブルが 1 つでもあれば、読み書きが終わるまでロックを持ち続ける**（今の単一接続の方式と同じ規則）。

検証した事実（`internal/source/parallel_snapshot_test.go`。MySQL 8.0 と 8.4 の両方で通った）:

- **同一時点になる**: ロックを持っている間は、書き込み側（別接続の `INSERT`）がブロックされるので、100 ms ずつずらして開始した 4 接続のスナップショットが、全て同じ内容を読む。ロックを使わない対照実験では、書き込みが続くと接続ごとに行数が食い違う。
- **ロック中でもスナップショットは開始できる**: `START TRANSACTION WITH CONSISTENT SNAPSHOT` と `SELECT` は、他の接続が持つグローバルな読み取りロックに止められない。
- **ロックを解放した後**: InnoDB のテーブルは、スナップショットが開始済みなので、解放後に書き込まれた行を読まない。MyISAM のテーブルはスナップショットに入らないので、解放後の書き込みが見える（検証では 3 行が 4 行になった）。したがって InnoDB 以外のテーブルがあるときは、ロックを持ち続ける。
- **必要な権限**: `FLUSH TABLES WITH READ LOCK` は `RELOAD` が要る。無いと error 1227 で失敗する。スナップショットの開始と読み出しは `SELECT` だけでよい。`SELECT` と `RELOAD` があれば方式の全体が動く。
- **書き込みを止める時間**: ロックの取得から、全接続のスナップショット開始までの時間。実測では接続 4 本で、接続の間に 100 ms 置いても 約 0.45 秒（間隔の 400 ms を除くと約 40 ms）。ただし `FLUSH TABLES WITH READ LOCK` は、実行中の長い文を待ってから取得される。実測では 2 秒の文に約 1.7 秒待たされた（その間は、後から来た書き込みも止まる）。InnoDB だけなら、止める時間は取得の待ちと接続の数に比例する開始時間だけで、読み出しの長さには依存しない。InnoDB 以外のテーブルがあると、読み出しの全期間になる。
- **InnoDB 以外のテーブルへの影響**: MyISAM などは、ロックを持っている間だけ一貫する。ロックを解放した後は、他の接続の書き込みが見える。このため上の規則（持ち続ける）にした。

【判断】採用しなかった案: `LOCK INSTANCE FOR BACKUP`（8.0 以降）は DDL だけを止め、DML を止めないので、位置を固定できない。ロックなしで各接続が順にスナップショットを開始する方式は、対照実験のとおり食い違う。

【実装】B-042。`source.Open` の `Config.Jobs`（2 以上）が上の手順をそのまま行う。`Snapshot.Readers()` が読み出し接続（先頭は `Snapshot` 自身）を返し、`dump` はテーブルごとに 1 接続を割り当てて、行の読み出し・描画・一時ファイルへの書き出しだけを並列にする。スキーマ・メタデータ・警告は先頭の接続で直列に読むので、出力は並列度によらない。方式から決まる点は次のとおり。
- 2 以上のときは InnoDB だけでも常に `FLUSH TABLES WITH READ LOCK` を使う（接続を揃えるため）。`RELOAD` が要り、無ければ `RELOAD` と `--jobs 1` を挙げたエラーで失敗する。
- `--no-lock` とは併用できない（使い方のエラー）。ロックなしではスナップショットが揃わず、黙って不整合にも、黙って直列にもしない。
- 非 InnoDB の判定は、ロックを取った後に行う（ロックは DDL を止めるので、判定と読み出しの間に競合しない）。
- 既定は 1。今までの経路（ロック用の接続を分けず、InnoDB だけならロックなし）をそのまま通る。
- 失敗や Ctrl-C のときは、読み出しの全接続とロック用の接続を閉じる。複数のテーブルが失敗したときは、失敗したテーブルのどれかのエラーを返す（最初の失敗が他を止めるので、どれかは時間で変わりうる）。止められたことによるキャンセルのエラーは返さない。

注意: 手順 3 で InnoDB だけのときにロックを解放すると、解放後は DDL（`ALTER` や `DROP TABLE`）が走りうる。スナップショットはカタログ（`information_schema`、`SHOW CREATE`）を覆わないので、これは今の単一接続の方式（README 参照）と同じ限界である。ロックを解放する前に、全テーブルのエンジンを確かめる必要がある（今の `source.Open` と同じ規則）。

確かめていないこと: `RELOAD` 以外の権限（8.0 以降の動的権限 `FLUSH_TABLES` など）で `FLUSH TABLES WITH READ LOCK` が通るか、MySQL 5.7 と MariaDB での同じ検証（`FLUSH TABLES WITH READ LOCK` 自体は通る）、レプリカ上での動作、GTID やバイナリログの位置との対応、接続数が数十本のときの開始時間、クラウドのマネージド MySQL（`RELOAD` を付けられないことがある）。

## 7. CLI

- 【判断】`mysqldance` コマンドが接続情報（host / port / user / password / socket）、対象 DB（複数指定可）、出力ディレクトリを受け取る。詳細なフラグ名は実装者が決める。
- 接続情報の受け渡し方法（`--defaults-file`、環境変数等）は mysqldump に倣う。パスワードをコマンドラインに必須にしない。

## 8. 将来拡張のための構造

- 内部では「テーブル定義」「列の型情報」「型付きのレコード値」を構造として持つ。SQL 文字列を直接組み立てるのではなく、レコードの値を構造化したうえで MySQL 向け出力に変換する。これにより PostgreSQL / SQLite 出力（boolean への変換、制約を後付けする順序など）を後から足せる。
- 出力ライタは差し替え可能なインターフェースにする。
- 取得部はテーブル単位で独立に呼び出せるようにして、将来の並列化に備える。
- ファイル名規約に、範囲分割の番号を足せる余地を残す。

## 9. テスト方針（実装者への指針）

- 値の表現（エスケープ、バイナリ、日時、NULL、巨大値）のラウンドトリップ検証: ダンプ → リストア → 元とデータが一致することを実 MySQL で確認する。
- 決定性の検証: 同じ DB を2回ダンプして出力が完全一致する。1レコードを更新したとき、差分が該当する1行だけになる。
- Docker の MySQL 公式イメージに出力ディレクトリをマウントして初期化できる。
- 主キーなしテーブル、複数 DB、外部キーの循環参照、ビューの依存を含む DB を用意する。

## 10. 実装者に任せること

MySQL ドライバ・CLI ライブラリの選定、パッケージ構成、フラグの細部、デフォルトの INSERT 分割上限、ファイル名エスケープ規則、AUTO_INCREMENT の扱い、ビュー依存解決のフォールバック。

## 10.5 レビューで確定した判断（すべて【判断】）

- **ビューの DEFINER**: `DEFINER=...` 句は出力から除く（リストア先にユーザがいなくても通り、ダンプ元のユーザ名が差分に出ないため）。`SQL SECURITY` は元の定義のまま残す。
- **固定ヘッダーのセッション設定**: `sql_mode`（固定値）、`time_zone='+00:00'`、文字コード（`utf8mb4`）、`FOREIGN_KEY_CHECKS=0`、`UNIQUE_CHECKS=0` を明示する。ダンプ側の接続も同じ `time_zone` に揃えて読み出す。`TIMESTAMP` 列の値が実行環境で変わらないようにするため。ダンプ側の接続ではさらに `sql_mode=''`・`sql_quote_show_create=1`・`show_create_table_verbosity=0` をセッションで固定する（mysqldump と同じく `sql_mode` を空にする。サーバー既定の `ANSI_QUOTES` で識別子が `"t"` になったり、`PAD_CHAR_TO_FULL_LENGTH` で `CHAR` の値が埋められたりしないように）。
- **`Rows` の戻り値**: 【実装】`source.Rows` は `[][]sqlvalue.Value` を返す。`Value` は列の型名・サーバーが返したバイト列（コピー）・NULL かどうかを持つ。MySQL 向けのリテラル化は `Value.Literal()` で、`dump` がデータファイルへ渡す直前に呼ぶ。キーのないテーブルの並べ替えキーは、従来どおりリテラルをカンマでつないだ文字列で、出力はバイト単位で変わらない。
- **列単位の権限**: 読めない列があるテーブル（`information_schema.COLUMNS` は権限のある列しか出さないが、`SHOW CREATE TABLE` は全列を出す）は、列を黙って落とさずエラーにする。
- **リストア先**: 空の DB（初期化）が主用途。スキーマファイルは mysqldump と同様に `DROP TABLE IF EXISTS` / `DROP VIEW IF EXISTS` を `CREATE` の前に置く。データファイルに `TRUNCATE` / `DELETE` は付けない。
- **AUTO_INCREMENT**: `CREATE TABLE` から `AUTO_INCREMENT=N` を除き、カウンタの値は復元しない。リストア後は最大値 + 1 から採番される。末尾の行を削除した履歴があると元と採番がずれるが許容し、README に明記する。
- **行の並び順の比較**:
  - 主キー / NOT NULL ユニークキーがあるテーブルは、`ORDER BY` でキー順に取得する。文字列キーは照合順序で順序が変わらないよう、バイナリ比較（`BINARY` 等）で並べる方式を採る。具体的な書き方（インデックスが使えなくなる場合の性能影響を含む）は実装者が決める。
  - NULL 可のユニークキーしかないテーブルは「キーなし」として扱う。
  - キーがないテーブルは、各行を出力する文字列に直し、そのバイト列を Go 側で昇順ソートする（メモリ内ソート）。重複行はそのまま並ぶ。
- **生成列 / 特殊な型**: 生成列は出力から除外する。生成列または `INVISIBLE` 列を持つテーブルは、`INSERT INTO t (col1, col2, ...) VALUES` と列リストを付ける。それ以外は列リストなし。`ENUM` / `SET` / `DECIMAL` / `GEOMETRY` 等の値の表記は mysqldump に合わせる（`GEOMETRY` や `BLOB` は hex）。
- **対象 DB**: 対象 DB は明示指定のみ（`information_schema` / `mysql` / `sys` / `performance_schema` は指定されてもエラー）。InnoDB 以外のストレージエンジンのテーブルが含まれる場合は、一貫性が保証されない旨を警告して続行する。権限が足りずに見えないテーブルがあればエラーにする。
- **ファイル名の接頭辞衝突**: `mydb` と `mydb2` のような DB 名の組み合わせで `.00-schema.sql` / `.10-data.` の並びが崩れないことを、エスケープ規則の設計制約とする（DB 名とそれ以降を区切る `.` を、DB 名内では必ずエスケープする等）。
- **【決定】想定データ規模（MVP）**: 1 DB あたり 10 テーブル程度、最大テーブルで 10 万行程度。この規模なら、主キーなしテーブルのメモリ内ソートや 1 接続の長いトランザクションを許容する。大規模データ向けのストリーム処理は将来拡張。ただし規模を大きく超えたときは警告を出すこと（閾値は実装者が決める）。【実装】推定行数（`TABLE_ROWS`）が 50 万行を超えるテーブルは、キーがないものだけ警告する（全行をメモリに読んで並べ替えるため）。キーのあるテーブルは B-044 で行ストリームになり、警告しない。
- **【決定】対象バージョン（MVP）**: MySQL 8.0 系以降（8.4 を含む）。5.7 系は MVP では対象外。MariaDB は B-038 で対応した（10.11 と 11.4 で確認。バージョンでは拒否しない。`SEQUENCE` / `SYSTEM VERSIONED` はエラー）。将来、古いバージョン（5.7 系等）のデータを持ってきたい意向があるので、バージョン依存の処理（ビュー依存解決、`CREATE TABLE` の出力形式の差など）は 1 か所にまとめて差し替えやすくしておく。テストは 8.0 系と 8.4 系で行う（【判断】）。

## 11. 未確認事項（実機で確かめる）

確認日: 2026-11-09。確認には `mysqldance` 本体（Task 1〜6 の実装）を使った。

- 【確認済み】公式イメージが `docker-entrypoint-initdb.d` のサブディレクトリを辿らず、辞書順で実行すること。
  - 確認したバージョン: `mysql:8.0`（8.0.46）と `mysql:8.4`（8.4.13）。
  - 方法: テスト用 DB（改行・引用符・バックスラッシュ・日本語を含む文字列、BLOB / VARBINARY / BIT、外部キー、依存する 2 段のビュー）を持つ MySQL を起動し、本ツールで出力ディレクトリを作り、新しい（データのない）`mysql` コンテナの `/docker-entrypoint-initdb.d` に渡して初回起動した。
  - 結果（平置き）: エントリポイントのログが `vdb.00-schema.sql` → `vdb.10-data.blobs.sql` → `vdb.10-data.orders.sql` → `vdb.10-data.users.sql` の順に `running` を出し、エラーなしで完了した。テーブル 3 つとビュー 2 つが作られ、全行が入った。行数は元の DB と復元後で一致（`users` 3 / `blobs` 4 / `orders` 2 / ビュー `v_users` 3 / `v_top` 2。両バージョンとも）。さらに `mysqldump --hex-blob --skip-extended-insert` のダンプ同士（元の DB と復元後の DB）の差分なしで確認した（文字列のエスケープ、BLOB、BIT、外部キー、ビューも一致。比較は `--hex-blob` の表記上のもので、空の BLOB は両方 `_binary ''` と出るため、空と NULL の区別は保たれている）。
  - 結果（サブディレクトリ）: 同じファイルを `/docker-entrypoint-initdb.d/dump/` の下に置くと、ログに `[Warn] [Entrypoint]: /usr/local/bin/docker-entrypoint.sh: ignoring /docker-entrypoint-initdb.d/dump` が出て何も実行されず、データベースは作られない。4.1 節の「フラットな 1 ディレクトリ」の前提は正しい。
  - 注: この環境の Docker デーモンは別ホストにあり、ホストのディレクトリのバインドマウント（`-v <dir>:/docker-entrypoint-initdb.d`）ではコンテナ内の `ls -la /docker-entrypoint-initdb.d` が `total 0`（ディレクトリは存在するが中身が空）だった。マウント自体の失敗ではなく、デーモン側からホストのパスが見えないためと考えられる（原因までは調べていない）。そのため `docker create` + `docker cp <dir>/. <container>:/docker-entrypoint-initdb.d/` + `docker start` で渡した。ローカルの Docker なら通常のバインドマウントで同じ結果になるはずだが、バインドマウント自体は今回の環境では未確認。
- 【差異あり】`BIT`・`BLOB` などの mysqldump における表記（8.0.46 / 8.4.13 の `mysqldump` で確認）。5 節の本文を直した。
  - `BLOB` / `VARBINARY`: `--hex-blob` ありなら `0x00FF10` のように `0x` + 16 進で、本ツールと同じ。`--hex-blob` なしだと `_binary '...'`（バイナリがそのまま入る）になる。空の BLOB は `--hex-blob` ありでも `_binary ''`（本ツールは `''`）。
  - `BIT(5)`: 設計時の想定（`b'...'`）と違い、mysqldump は `--hex-blob` ありなら `0x15`、なしなら `_binary '...'` に 0x15 の 1 バイトをそのまま入れて出す。`b'10101'` 形式にはならない。本ツールは `b'10101'`（`b'1'` のように先頭の 0 は省く）で出す。両方とも復元後の値は同じ（`BIN(bits)` で確認）。本ツールの `b'...'` は 16 進より値が読みやすく、バイナリ文字を含まないので意図的な差異として残す。
  - 空の BLOB: mysqldump は `_binary ''`、本ツールは `''`。これも意図的な差異として残す（本ツールは 16 進かプレーンなリテラルだけを出し、空の値は `''` と書く）。復元結果は、本ツールの出力から復元した行で `hex(b)` が空文字列（NULL ではない）になること、および `--hex-blob` のダンプ同士の差分が空であること（両方 `_binary ''`）で確認した。
  - 文字列（`'...'`）の `\n` `\'` `\"` `\\` のエスケープ、NULL は mysqldump と本ツールで一致した。
- 【確認済み】`information_schema.VIEW_TABLE_USAGE` は MySQL 8.0.46 と 8.4.13 の両方で使え、2 段のビュー（`v_top` → `v_users` → `users`）の依存が取れた（`SELECT view_name, table_name FROM information_schema.VIEW_TABLE_USAGE`）。Task 5 の統合テスト（`internal/source` の統合テスト。`task test:integration` で 8.0 と 8.4 の両方を起動して実行）でも同じ 2 バージョンで使われ、通っている（Task 5 のレビュー時の実行結果に基づく。今回は再実行していない）。8.0.13 より前の版は確認していない（対象外）。
- 【差異あり】`FLOAT` / `DOUBLE` のラウンドトリップ（確認日: 2026-11-10。`mysql:8.0` と `mysql:8.4` で確認）。`DOUBLE` は `-0` を除いて一致するが、`FLOAT` は一致しない。`-0` は両方で `0` に戻る。5 節の「浮動小数点の表記」の検証結果。
  - 方法: `internal/dump` の統合テスト `TestFloatDoubleRoundTrip`。`FLOAT` と `DOUBLE` の列を持つ表に、境界値（`0.1`、`1e38`、`3.4028235e38`（FLOAT の最大値）、`1.17549435e-38`（FLOAT の最小の正規化数）、`1.7976931348623157e308`（DOUBLE の最大値）、`2.2250738585072014e-308`、`16777216`、`0.30000000000000004` など）と、`0`・`-0`・サブノーマル数（`1e-40`、`1e-310`、各型の最小の正の値）、固定のシードで作った乱数のビット列（NaN と無限大を除く）を合わせて 300 行入れ、ダンプ → 元の DB を削除 → 復元した。読み出しはバイナリプロトコル（プリペアドステートメント）で行い、IEEE 754 のビット列を比較した。
  - `DOUBLE`: `-0` の 1 行を除く 299 行がビット単位で一致した（両バージョン。サブノーマル数も一致）。テキスト表記は往復に十分な桁数で出ていた（桁数の決め方までは調べていない）。`-0`（`0x8000000000000000`）は `0` になり符号が落ちる（原因は未調査。サーバーの表記かリテラルの扱いかは切り分けていない）。
  - `FLOAT`: 300 行中 279 行が不一致だった（両バージョン。`-0` の 1 行を含む。件数は乱数の生成方法に依存する）。サーバーのテキストプロトコルは `FLOAT` を有効数字 6 桁で出すため、7〜9 桁が必要な値で下位ビットが落ちる。例: 最大値 `3.4028235e38`（`0x7f7fffff`）は `3.40282e+38`（`0x7f7fffee`）、`16777216`（`0x4b800000`）は `1.67772e+07`（`0x4b7ffff0`）、`0.33333334`（`0x3eaaaaab`）は `0.333333`（`0x3eaaaa9f`）として出力され、復元後に値が変わる。`0.1` のような 6 桁で収まる値は一致する。
  - 対応（B-059）: 修正した。`-0` の原因は、サーバーが `-0` と出力し、その `-0` が整数 `0` の単項マイナスとして復元されること（`-0e0` なら符号が残る。8.4 で確認）。`FLOAT` は `` `f` * 1e0 `` で正確な `DOUBLE` として読み（`CAST(f AS DOUBLE)` は 5.7 と 8.0.17 より前で使えない）、`float32` の最短表記で出す。`FLOAT` の最大値は `3.4028235e38` と書くと `DOUBLE` として範囲外になり、サーバーが拒否する（8.4 で確認）ので、その場合は正確な `DOUBLE` の表記にする。以下は B-016 の時点の記録。
  - 対応（B-016、当時）: この項目（B-016）は検証だけを行った。修正は B-059 に積んだ。なお `CAST(f AS DOUBLE)` は `0.1` を `0.10000000149011612` にするので、そのまま使うと表記が変わり読みにくい。有効数字 9 桁で出す方法なども含めて B-059 で決める。テストは既知の不一致（`FLOAT` の 6 桁への丸めと `-0`）だけを許して skip し、それ以外の不一致は失敗にする。出力が正確になれば skip せず通る。
- 【差異あり】MariaDB と MySQL の差異（B-037。確認日: 2026-11-10。`mariadb:10.11`（10.11.19）と `mariadb:11.4`（11.4.13）の 2 つの LTS を、比較用の `mysql:8.4`（8.4.13）と並べて確認した）。12 節の「記憶ベースで未確認」だった差異を実機で確かめた。コードは変えていない。
  - 方法: 同じ SQL（生成列、`INVISIBLE` 列、外部キー、CHECK、降順・接頭辞インデックス、ビュー、JSON 列、式の既定値、シーケンス、システムバージョニング、`UUID` / `INET6` / `INET4` 型）を 3 つのサーバーに流し、`SHOW CREATE` と、`dialect` の全クエリ（`internal/source/dialect.go`）の結果を比べた。さらに、現在のバイナリを 11.4 につなぎ、現状の挙動を確かめた。
  - 現状の挙動（確認済み）: 認証と接続は通る（`root` のパスワード認証）。その後 `SET SESSION show_create_table_verbosity = 0` が `Error 1193: Unknown system variable` で失敗し、`dialectFor` に着く前に終わる。この変数は MariaDB の 10.11 にも 11.4 にもない。`SET SESSION sql_mode = ''`、`sql_quote_show_create = 1`、`SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ`、`START TRANSACTION WITH CONSISTENT SNAPSHOT` は両方で通る。`dialectFor` の MariaDB 拒否は、この `SET` より後ろにあるので、今は効いていない（B-038 で順序を見直す）。
  - `dialect` のクエリ: `VIEW_TABLE_USAGE` だけが使えない（`ERROR 1109: Unknown table 'VIEW_TABLE_USAGE' in information_schema`。両バージョン）。`tables` / `columns` / `uniqueKeys` / `defColumns` / `defIndexes` / `defForeignKeys` / `defChecks` は、そのままの SQL で動き、列の形も同じ。代替は `information_schema.VIEWS.VIEW_DEFINITION`（`` `probe`.`p` `` のように修飾した名前で出る）の解析になるが、`SHOW CREATE VIEW` の本文（修飾なし）とは形が違う。解析で済ませるか、ビューの依存解決を別の方式にするかは B-038 で決める。
  - 同じ SQL でも**意味が違うもの**（`dialect` の SQL を直しても足りない）:
    - `COLUMN_DEFAULT`: 既定値が `NULL` の列（`DEFAULT NULL`）は、MySQL では SQL の `NULL` だが、MariaDB では**文字列 `"NULL"`**（`COLUMN_DEFAULT IS NULL` が 0）。既定値なしの `NOT NULL` 列は SQL の `NULL`。文字列の既定値は MySQL では `x`、MariaDB では**引用符つき** `'x'`（`a'b` は `'a''b'`）。式の既定値は `current_timestamp()`、`` (`id` + 1) ``、`uuid()` のように**式のテキストがそのまま**入り、`EXTRA` に `DEFAULT_GENERATED` が付かない（MySQL は `EXTRA = DEFAULT_GENERATED`）。ビューの列には `0` のような既定値が付くことがある。今の `defColumns`（`internal/source/def.go`）は `Valid` なら既定値とみなし、`DefaultExpr` を `EXTRA` の `default_generated` で決めるので、MariaDB では `NULL` 既定値を文字列 `NULL` として、式を文字列として扱い、SQLite / PostgreSQL 向けの出力を誤る。方言ごとに既定値の読み方を持つ必要がある。
    - `EXTRA`: `on update current_timestamp()`（MariaDB）と `on update CURRENT_TIMESTAMP`（MySQL）。現在のコードは小文字に直して比べるので影響しない。`VIRTUAL GENERATED` / `STORED GENERATED` / `INVISIBLE` は同じ。
    - `COLUMN_TYPE`: 整数に表示幅がつく（MariaDB `int(11)`、MySQL 8.0.19 以降 `int`）。`DATA_TYPE` は同じ（`int`）。`COLUMN_TYPE` を使う処理は表示幅を考える必要がある。
    - `TABLE_TYPE`: 通常のテーブルとビューのほかに、**`SEQUENCE`** と **`SYSTEM VERSIONED`**（システムバージョニングのテーブル）がある。今のコードは `BASE TABLE` と `VIEW` だけを拾う（`source.go`、`def.go`）ので、この 2 種は**黙って出力から落ちる**。「黙って落とさない」に反するので、B-038 でエラーにするか出力するかを決める。
  - `SHOW CREATE` の書式（コードは `SHOW CREATE TABLE` の本文を `schemafile` で正規化して使う）:
    - 既定値: `DEFAULT current_timestamp()`（小文字と括弧）、`ON UPDATE current_timestamp()`、`DEFAULT uuid()`、`DEFAULT (` `` `id` + 1 `` `)`。MySQL は `DEFAULT CURRENT_TIMESTAMP`、`DEFAULT (uuid())`、`DEFAULT ((` `` `id` + 1 `` `))`（二重括弧）。数値の既定値も MariaDB は `DEFAULT 0`、MySQL は `DEFAULT '0'`。バイナリの既定値は `SHOW CREATE TABLE` では両方 `DEFAULT 'AB'` だが、`COLUMN_DEFAULT` は MariaDB が `'AB'`、MySQL が `0x4142`。
    - 整数の表示幅（`int(11)`）、`CHECK (` `` `n` >= 0 `` `)`（MySQL は二重括弧 `((` `` `n` >= 0 `` `))`）、生成列 `GENERATED ALWAYS AS (` `` `n` + 1 `` `)`（MySQL は二重括弧）。
    - `INVISIBLE`: MariaDB は `` `hidden` int(11) INVISIBLE DEFAULT NULL ``（コメントに包まない）。MySQL は `` `hidden` int DEFAULT NULL /*!80023 INVISIBLE */ ``。
    - 外部キー: 既定の `ON UPDATE RESTRICT` は MariaDB では出ない（MySQL は `ON DELETE CASCADE ON UPDATE RESTRICT` と書く）。意味は同じ。
    - 降順インデックスは `KEY ` `` `k_desc` `` ` (` `` `name` `` ` DESC)` で、両方とも同じ。接頭辞キーも同じ。
    - JSON: MariaDB の `JSON` は別名で、`SHOW CREATE TABLE` は `` `doc` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL CHECK (json_valid(`doc`)) `` と出る。`information_schema.COLUMNS` の `DATA_TYPE` は `longtext`（`json` ではない）で、`TABLE_CONSTRAINTS` に列と同じ名前の CHECK（`doc`）が増える。値の読み出しは文字列のまま（`{"a": 1,   "b": [1,2]}` の空白もそのまま。MySQL の `JSON` は正規化された表記で返る）。
    - 照合順序: 10.11 の既定は `utf8mb4_general_ci`、11.4 の既定は `utf8mb4_uca1400_ai_ci`（`SHOW CREATE DATABASE` と `SHOW CREATE TABLE` の `COLLATE=`）。MySQL 8.4 は `utf8mb4_0900_ai_ci`。MariaDB の出力を MySQL に流すと、`utf8mb4_uca1400_ai_ci` が MySQL にないので失敗する。MySQL の出力を MariaDB に流しても `utf8mb4_0900_ai_ci` がなく失敗する。`SHOW CREATE DATABASE` の末尾にある `/*!80016 DEFAULT ENCRYPTION='N' */` は MariaDB にはない。
    - `SHOW CREATE VIEW`: 結果の 2 列目が定義である点は同じ。ビュー本文は MariaDB が `on(` `` `c`.`pid` = `p`.`id` `` `)`、MySQL が二重括弧。`character_set_client` と `collation_connection` は MariaDB が `utf8mb3` 系、MySQL が `latin1` 系（接続のセッション値による）。
  - MariaDB だけにあるもの: シーケンス（`CREATE SEQUENCE`。`SHOW CREATE SEQUENCE` と、1 行のテーブルのような `SELECT *`。`SHOW CREATE TABLE` は `SEQUENCE=1` で出る）。システムバージョニング（`WITH SYSTEM VERSIONING`。`SHOW CREATE TABLE` の末尾に付き、`SELECT *` は現在の行だけで履歴行は返らない。`FOR SYSTEM_TIME ALL` では 2 行。行の開始・終了の列は `information_schema.COLUMNS` に出ない）。`UUID` / `INET6` / `INET4` 型（`DATA_TYPE` は `uuid` / `inet6` / `inet4`。値はテキストで読める。`HEX(u)` は大文字の 16 進）。
  - 確認していないこと: `FLUSH TABLES WITH READ LOCK` と非 InnoDB（Aria など）の挙動、権限の違い、`ed25519` などの認証プラグイン、MariaDB 10.6 以前、MySQL 8.0 とのデータの読み出し差（`DECIMAL`、`FLOAT`、`BIT`、日時の小数秒）。
  - 方針: **`dialect` に集約する**。ただし `dialect` は今のところ SQL 文字列だけなので、**SQL に加えて、結果の読み方を持つ**必要がある。具体的には、(1) セッションの設定（`show_create_table_verbosity` を MySQL だけにする）、(2) ビュー依存の取得（`VIEW_TABLE_USAGE` か、`VIEW_DEFINITION` の解析）、(3) 既定値の読み方（`NULL` の文字列、引用符、式の判定）、(4) 対応しない `TABLE_TYPE`（`SEQUENCE` / `SYSTEM VERSIONED`）の扱い。それ以外のコードに MariaDB 固有の分岐は入れない。出力の書式の差（`SHOW CREATE TABLE` の本文）は、MySQL 向け出力では**そのまま MariaDB の書式で出す**（MariaDB から MariaDB へ戻すための出力とし、MySQL への互換変換はしない）。照合順序の非互換は、README に注意として書く。
- 【差異あり】MySQL 5.7 と 8.x の差異（B-039。確認日: 2026-11-10。`mysql:5.7`（5.7.44）を、比較用の `mysql:8.4`（8.4.13）と並べて確認した。`show_create_table_verbosity` だけ `mysql:5.7.21` でも確認した）。コードは変えていない。
  - 方法: B-037 と同じ流れ。生成列（`VIRTUAL` / `STORED`）、JSON、BIT、BLOB、DECIMAL、`DATETIME(3)`、`TIMESTAMP(6)`、外部キー、接頭辞インデックス、MyISAM、2 段のビューを持つ DB を両方に作り、`SHOW CREATE` と `dialect` の全クエリの結果を比べた。さらに、5.7 の `dialectFor` の拒否だけを外した一時的なバイナリ（リポジトリには入れていない）で `--target mysql` / `sqlite` / `postgres` の出力を 8.4 と比べ、5.7 の出力を 5.7 と 8.4 に流して復元した。5.7 のイメージは `linux/amd64` のみで、arm64 の Docker では `--platform linux/amd64`（エミュレーション）で起動した。
  - 現状の挙動（確認済み）: 接続とセッション設定は通り、`dialectFor` が「8.0 以上が必要」で拒否する。拒否を外して試すと、次に `VIEW_TABLE_USAGE` で `Error 1109: Unknown table 'VIEW_TABLE_USAGE' in information_schema` が出て失敗する。
  - `dialect` のクエリ（5.7.44）: `VIEW_TABLE_USAGE` だけが使えない。`tables` / `columns` / `uniqueKeys` / `defColumns` / `defIndexes` / `defForeignKeys` / `defChecks` は、そのままの SQL でエラーなく動き、列の形も同じ。`INVISIBLE` 列、関数インデックス、降順インデックスは 5.7 にない（`INVISIBLE` と関数インデックスは構文エラー。`DESC` は構文は通るが無視され、`SHOW CREATE TABLE` にも出ない）。CHECK は構文は通るが無視され、`TABLE_CONSTRAINTS` に出ない（`defChecks` は 0 行で、エラーにならない）。生成列は 5.7.6 以降にあり、`EXTRA` は `VIRTUAL GENERATED` / `STORED GENERATED` で 8.x と同じ。
  - ビュー依存の代替: `information_schema.VIEWS.VIEW_DEFINITION` は、from 句のテーブルとビューを `` `probe`.`p` `` のようにスキーマで修飾して出す（列の参照は、ビューへのものは修飾されず `` `v1`.`name` ``）。B-038 の `viewDefinitions`（`` `schema`.`name` `` を探す）を、そのまま使って 5.7 でも正しく順序が取れた（2 段のビュー `v2` → `v1` → `p`）。
  - セッション設定: `SET SESSION show_create_table_verbosity = 0` は **5.7.22 から**ある。5.7.21 では `Error 1193: Unknown system variable` で失敗する。5.7.44 では通る（既定は `OFF`）。`sql_mode = ''`、`sql_quote_show_create = 1`、`time_zone`、`NAMES utf8mb4`、`TRANSACTION ISOLATION LEVEL REPEATABLE READ`、`START TRANSACTION WITH CONSISTENT SNAPSHOT` は 5.7.44 で通る（5.7.21 では `show_create_table_verbosity` の失敗しか見ていない。残りは未確認）。`FLUSH TABLES WITH READ LOCK` も 5.7.44 で通った（MyISAM の表がある DB で、`--no-lock` なしのダンプが成功）。
  - 同じ SQL でも**意味が違うもの**（`dialect` の SQL を直しても足りない）:
    - `COLUMN_DEFAULT` の式: 5.7 の `EXTRA` には `DEFAULT_GENERATED` が付かない（8.4 は `DEFAULT_GENERATED on update CURRENT_TIMESTAMP`）。`TIMESTAMP` / `DATETIME` の `DEFAULT CURRENT_TIMESTAMP` は `COLUMN_DEFAULT` に文字列 `CURRENT_TIMESTAMP`（精度つきは `CURRENT_TIMESTAMP(6)`。`NOW()` は `CURRENT_TIMESTAMP` に正規化される）として入るだけで、式かどうかを示す印がない。5.7 の既定値の式はこの `CURRENT_TIMESTAMP[(n)]` だけ（括弧つきの式の既定値は 8.0.13 から）。今の `mysqlColumnDefault` は `EXTRA` の `default_generated` だけで式を決めるので、5.7 では**式を文字列として扱う**。実際に、5.7 を `--target sqlite` で出すと `` `ts` TEXT NOT NULL DEFAULT 'CURRENT_TIMESTAMP' ``（`TIMESTAMP(6)` なら `'CURRENT_TIMESTAMP(6)'`）と**固定の文字列**になり、8.4 の `DEFAULT CURRENT_TIMESTAMP` と違う（行を入れたときに時刻にならない）。`--target postgres` では既定値が出ず、警告になる（8.4 は `DEFAULT CURRENT_TIMESTAMP`）。`--target mysql` は `SHOW CREATE TABLE` の本文を使うので影響しない。方言ごとの既定値の読み方（B-038 で入れた `columnDefault`）で、`DATA_TYPE` が `timestamp` / `datetime` で、`COLUMN_DEFAULT` が `CURRENT_TIMESTAMP` で始まるものを式にすれば足りる。ただし `columnDefault` は今 `DATA_TYPE` を受け取らないので、引数を足す必要がある（`varchar` の `DEFAULT 'CURRENT_TIMESTAMP'` と区別するため）。
    - `REFERENTIAL_CONSTRAINTS` の規則: 外部キーに `ON UPDATE` を書かないとき、5.7 は `UPDATE_RULE = RESTRICT`、8.4 は `NO ACTION`（`DELETE_RULE` も同様）。`SHOW CREATE TABLE` はどちらも何も書かない。SQLite 向けの出力は、5.7 が `ON UPDATE RESTRICT`、8.4 が `ON UPDATE NO ACTION` になり、**同じスキーマでもバージョンで出力が変わる**（意味は、即時チェックの外部キーでは実質同じ）。PostgreSQL 向けも同じ規則を使う。5.7 の `RESTRICT` を `NO ACTION` に直すかは、B-040 で決める。
    - `COLUMN_TYPE`: 整数に表示幅がつく（`int(11)`、`bigint(20) unsigned`、`tinyint(3) unsigned`、`mediumint(9)`、`year(4)`）。MySQL 8.0.19 以降は付かない。SQLite / PostgreSQL 向けの型の対応は、5.7 の表示幅つきの型でも 8.4 と**同じ出力**だった（`tinyint(1)` の扱いを含む。差分なし）。
  - `SHOW CREATE` の書式（`--target mysql` の出力の差。データファイルは 5.7 と 8.4 で**バイト単位で一致**した。時刻の値を除く）:
    - 整数の表示幅（上）。`utf8mb4` の列と表の `COLLATE` は、5.7 では既定の照合順序なら出ない（8.4 は `utf8mb4_0900_ai_ci` を書く）。
    - 既定の文字セット: 5.7 の `character_set_server` は `latin1`（`latin1_swedish_ci`）。`CREATE DATABASE` と `CREATE TABLE` に `DEFAULT CHARSET=latin1` が出る。接続は `SET NAMES utf8mb4` なので、`latin1` の列でもデータは `utf8mb4` で正しく読める（日本語や 4 バイト文字は `utf8mb4` の列で一致した。`latin1` の列に入る文字は `latin1` の範囲）。
    - `CREATE DATABASE` の末尾の `/*!80016 DEFAULT ENCRYPTION='N' */` は 5.7 にはない。
    - 生成列の式（`GENERATED ALWAYS AS ((` `` `n` + 1 `` `))`）、`DEFAULT '0'`、外部キー、接頭辞キー、`SHOW CREATE VIEW` の本文は、8.4 と同じ。CHECK と降順は、サーバーが捨てるので出ない。
  - 往復（確認済み）: 5.7 の出力（`--target mysql`）を、新しい 5.7 と新しい 8.4 の両方に流して、エラーなく復元でき、行数と値（`BIT`、`BLOB`、JSON、`DATETIME(3)`、文字列のエスケープ）が一致した。`int(11)` の表示幅は 8.4 で受け付けられた。
  - 確認していないこと: 5.7.44 以外の 5.7（5.7.21 は `SET` だけ。JSON は 5.7.8 から、生成列は 5.7.6 から、それより前は対象外）、5.6 以前、`FLUSH TABLES WITH READ LOCK` の非 InnoDB 以外の影響、グローバルに `show_create_table_verbosity = ON` と設定された 5.7.22 以降のサーバー（`SET` を省くと、その設定が `SHOW CREATE TABLE` に出る）、`lower_case_table_names` が 0 以外の 5.7、`mysql_native_password` 以外の認証、権限の違い、`DECIMAL` / `FLOAT` などの値の読み出し差（確認した値は `DECIMAL(10,2)` の `1.50` と `FLOAT` の `0.1` だけ）。
  - 方針: **`dialect` に集約する**（MariaDB と同じ）。5.7 向けの `mysql57` を、`mysql8` を元にした差し替えで作る。具体的には、(1) ビュー依存を `viewDefinitions` にする（B-038 のものを再利用）、(2) セッション設定から `show_create_table_verbosity` を外す（5.7.21 以前で失敗するため。5.7 は版を見分けずに外す。5.7.22 以降でグローバルが `ON` のサーバーは未確認で、README に書く）、(3) 既定値の式の判定を、`DATA_TYPE` を見て `CURRENT_TIMESTAMP[(n)]` を式にする方式にする（`columnDefault` に `DATA_TYPE` を渡す）、(4) `INVISIBLE` / CHECK / 降順は 5.7 にないので変えなくてよい（クエリは空か、`NULL` を返す）。外部キーの規則（`RESTRICT` と `NO ACTION`）の正規化は B-040 で決める。それ以外のコードに 5.7 固有の分岐は入れない。出力の書式の差（`int(11)`、`latin1`）は、MariaDB と同じく**そのまま出す**（互換変換はしない）。テストで確認するのは 5.7.44 だけにする（それ以外の 5.7 は未確認として README に書く）。5.7 のイメージは amd64 のみなので、統合テストは `--platform linux/amd64` で起動する必要がある（arm64 の開発機ではエミュレーションになり、起動が遅い）。
- 【差異あり】MySQL 5.6 と 5.7 の差異（B-079。確認日: 2026-11-12。`mysql:5.6`（5.6.51。公式イメージの最後の版）を、比較用の `mysql:5.7.44` と `mysql:8.4` と並べて確認した）。コードは変えていない。結論: **5.6.51 は、拒否を外すだけで、5.7 向けの `dialect`（`mysql57`）のままダンプでき、出力は新しい 8.4 と MariaDB に復元できる**。5.6 だけの注意点は、TLS（下）とゼロ日付（下）。
  - 方法: B-039 と同じ流れ。`dialect` の全クエリ（`internal/source/dialect.go`）を同じ DB に流して 5.6 と 5.7.44 の結果を比べ、`SHOW CREATE` の本文を比べた。DB は、整数、`BIT`、`BLOB`（空を含む）、`DECIMAL`、`FLOAT`、`DATETIME(3)`、`TIMESTAMP(6)`、`YEAR`、文字列のエスケープ、日本語と 4 バイト文字、外部キー、接頭辞キー、`DESC` つきのインデックス、`CHECK`、MyISAM、`latin1` のテーブル、2 段のビュー、ゼロ日付を持つ。5.6 の拒否を外した一時的なバイナリ（`dialectFor` の条件を 5.6 まで緩めた 1 行だけ。リポジトリには入れていない）で、`--target mysql` / `sqlite` / `postgres` の出力を取り、5.7.44 と比べた。復元先は、新しい `mysql:8.4`、`mariadb:10.11`、`mariadb:11.4`（どれも初期化したてのコンテナで、`sql_mode` は既定の厳格モードのまま）。5.6 のイメージは `linux/amd64` のみで、arm64 の Docker では `--platform linux/amd64`（エミュレーション）で起動した。
  - 現状の挙動（確認済み）: 接続は通る（`root` のパスワード認証。`--ssl-mode` の既定 `preferred` は、5.6 のイメージが TLS 無効なので、警告もログもなく平文になる）。その直後に `dialectFor` が `unsupported server version "5.6.51": MySQL 5.7 or later (or MariaDB) is required` で拒否する。この拒否はセッション設定より前なので、`SET SESSION show_create_table_verbosity` の `Error 1193` には着かない（拒否を外して `mysql57` を使うときは、この `SET` を送らない）。
  - セッション設定（`mysql` クライアントで 5.6.51 に送って確認）: `show_create_table_verbosity` だけが `Error 1193: Unknown system variable` で失敗する（5.7.21 と同じ）。`sql_mode = ''`、`sql_quote_show_create = 1`、`time_zone`、`NAMES utf8mb4`、`TRANSACTION ISOLATION LEVEL REPEATABLE READ`、`START TRANSACTION WITH CONSISTENT SNAPSHOT`、`FLUSH TABLES WITH READ LOCK` は通る。`FLUSH TABLES WITH READ LOCK` は、MyISAM のテーブルがある DB で、`--no-lock` なしのダンプが成功することでも確認した。
  - `dialect` のクエリ（5.6.51）: `VIEW_TABLE_USAGE` だけが使えない（`ERROR 1109: Unknown table 'VIEW_TABLE_USAGE' in information_schema`。5.7 と同じ）。それ以外の 7 つは、そのままの SQL でエラーなく動き、**列の形も結果も 5.7.44 と同じ**だった（`diff` で差分なし。`int(11)` などの表示幅、`EXTRA` に `DEFAULT_GENERATED` が付かないこと、外部キーの規則が `RESTRICT` であることも同じ）。`CHECK` は構文は通るが捨てられ（`TABLE_CONSTRAINTS` に出ない）、`DESC` も無視される（`SHOW CREATE TABLE` に出ない）。どちらも 5.7 と同じ。生成列（5.7.6 から）、JSON（5.7.8 から）、`INVISIBLE`、関数インデックスは 5.6 にない（マニュアルによる。5.6 の実機では試していない）。ビュー依存は、`viewDefinitions`（`VIEW_DEFINITION` の `` `schema`.`name` ``）で、2 段のビュー（`v2` → `v1` → `p`）の順序が正しく取れた（`VIEW_DEFINITION` の書式は 5.7 と同じ）。
  - `SHOW CREATE` の書式（`--target mysql`）: 5.7.44 の出力と、**スキーマファイルもデータファイルもバイト単位で一致した**（ゼロ日付と暗黙の `TIMESTAMP` の表を足す前の DB で `diff -r`）。整数の表示幅、`year(4)`、`DEFAULT CHARSET=utf8mb4`（DB を明示したので。サーバーの既定は `latin1` で、5.7 と同じ）、`ENCRYPTION` のコメントがないことも同じ。
  - 小数秒（5.6.4 以降）: `DATETIME(3)` と `TIMESTAMP(6)`（`DEFAULT CURRENT_TIMESTAMP(6)` を含む）は、5.7 と同じ出力で、復元後も値が一致した（`2020-01-02 03:04:05.123`、`2021-01-01 00:00:00.500000`）。5.6.4 より前は対象外。
  - ゼロ日付（**5.6 の既定で起きやすい**）: 5.6 の既定の `sql_mode` は `NO_ENGINE_SUBSTITUTION` だけで、`0000-00-00` や `2020-00-10` が格納できる（5.7 / 8.x の既定は厳格モードで、同じ値を入れられない）。さらに、5.6 は `explicit_defaults_for_timestamp = OFF` で、`TIMESTAMP NOT NULL` の 2 つ目以降の列に `DEFAULT '0000-00-00 00:00:00'` が暗黙につく（5.7 の既定も `OFF` だが、厳格モードの CREATE で `Error 1067: Invalid default value` になる。5.6 の `CREATE TABLE ... (a TIMESTAMP, b TIMESTAMP)` はこうして作れ、`SHOW CREATE TABLE` に `DEFAULT '0000-00-00 00:00:00'` が出る）。本ツールは値をそのまま `'0000-00-00'` と出す（`--target mysql`）。復元は、データファイルとスキーマファイルが `SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO'` で始まり、厳格モードを外すので、8.4 と MariaDB の既定の厳格モードのサーバーでも通った（ゼロ日付の列も `DEFAULT '0000-00-00 00:00:00'` の列も、そのまま入った）。`--target sqlite` は、`TEXT` の列に `'0000-00-00 00:00:00'` をそのまま入れ、既定値も `DEFAULT '0000-00-00 00:00:00'` と出る（SQLite は日付を検査しない）。`--target postgres` は、ゼロ日付の既定値を警告して出さず（`default of column ... is not output`）、**ゼロ日付の値は `"0000-00-00 00:00:00" is not a date and time PostgreSQL accepts` のエラーで失敗する**（黙って落とさない。これは 5.6 に限らない今の挙動）。
  - TLS: 公式の `mysql:5.6` は、証明書を自動生成しない（5.7.6 から）ので `have_ssl = DISABLED` で起動する。このとき `--ssl-mode required` は `TLS requested but server does not support TLS` で失敗し、`preferred`（既定）は黙って平文になる。**証明書を設定すれば 5.6.51 は本ツールの下限（TLS 1.2）でつながる**: 自分で作った CA とサーバー証明書（RSA 2048。秘密鍵は従来の RSA 形式にして渡した。PKCS#8 で足りるかは未確認）を `--ssl-ca` / `--ssl-cert` / `--ssl-key` で渡して起動した 5.6.51 に、`--ssl-mode required` と `--ssl-mode verify-identity --ssl-ca` の両方でハンドシェイクと認証が通った（空の DB だったので、そのあとは `database "probe" not found`。TLS の上での全量のダンプは試していない）。`openssl s_client` では、TLS 1.2（`ECDHE-RSA-AES256-GCM-SHA384`）と TLS 1.1（`ECDHE-RSA-AES256-SHA`）の両方が通った。5.7.21（TLS 1.1 まで。B-082）と違い、5.6.51 は `--ssl-mode disabled` にしなくてよい。5.6.51 より前の版（TLS 1.2 を持たないものがありうる）は未確認。
  - 往復（確認済み）: 5.6 の出力（`--target mysql`）を、新しい 8.4（8.4.x）、MariaDB 10.11、MariaDB 11.4 に流して、どれもエラーなく復元でき、行の値が一致した（`BIT`、空と `0x00FF10` の `BLOB`、`DECIMAL(10,2)`、`FLOAT`（`0.1`）、`DATETIME(3)`、`TIMESTAMP(6)`、`YEAR`、文字列のエスケープ、日本語と 4 バイト文字、`NULL`、ゼロ日付、MyISAM、`latin1` のテーブル、外部キー、2 段のビュー）。ビューは、3 つの復元先とも `v1` と `v2` が作られ、`v2` が 2 行を返した（元と同じ行数。行の突き合わせまではしていない）。MariaDB は `int(11)` と `year(4)` を受け付け、8.4 は表示幅を捨てて `int` と `year` にする（値は同じ）。照合順序は復元先の既定（8.4 は `utf8mb4_0900_ai_ci`、MariaDB 10.11 は `utf8mb4_general_ci`、11.4 は `utf8mb4_uca1400_ai_ci`）になる（出力に `COLLATE` がないため）。
  - 確認していないこと: 5.6.51 以外の 5.6（5.6.4 より前は小数秒がない。`SET` と `dialect` は 5.6.51 だけ）、5.5 以前（B-080）、`--jobs` 2 以上と `--no-lock`（5.7 の節と同じく、未確認）、グローバルな `sql_mode` が厳格な 5.6、`lower_case_table_names` が 0 以外、`mysql_native_password` 以外の認証（`old_password` を含む）、権限の違い、TLS の上での全量のダンプ、`latin1` の列に入った UTF-8（8.4 で 14 節に記録した。5.6 では未確認）、`DECIMAL` / `FLOAT` の値の読み出し差（確認した値は上の 2 つだけ）。
  - 方針: **5.6 は `mysql57` をそのまま使う**。具体的には、(1) `dialectFor` の条件を 5.6 まで緩める（`major == 5 && minor` が 6 または 7。拒否のメッセージは「MySQL 5.6 or later」にする）。(2) `dialect` の中身は変えない（セッション設定、ビュー依存、既定値の読み方は 5.7 と同じでよい）。(3) 出力の書式の差（`int(11)`、`latin1`）は、5.7 と同じく**そのまま出す**。(4) テストで確認するのは 5.6.51 だけにする。5.7 と同じく専用のタスク（`mysql:5.6`、`--platform linux/amd64`）にして、`task test:integration` の既定には含めない。(5) README には、5.6 の注意（確認した版が 5.6.51 だけであること、TLS を有効にしないと `preferred` は平文になること、ゼロ日付が `--target postgres` で失敗すること）を書く。TLS の下限を下げる件は B-082、5.5 は B-080 で扱う。
- 【確認済み】SQLite 向け出力を `sqlite3` コマンドで復元できること（B-069。確認日: 2026-11-12。`mysql:8.0` / `mysql:8.4` から出力）。
  - 方法: 統合テスト `TestSQLiteRestoreWithCommand` と `TestSQLiteRestoreWithCommandSplitTable`（`internal/dump/sqlite_cli_test.go`）。README と同じ `LC_ALL=C cat *.sql | sqlite3 file` で復元し、`modernc.org/sqlite` で開いて、`modernc` での復元と同じ表・問い合わせで MySQL と値を比べた。`sqlite3` の標準エラー出力が空でなければ失敗にする（`sqlite3` は失敗した文のあとも続けるため）。`sqlite3` が無い環境は skip し、CI（`integration.yml`）はログに skip の文言が残れば失敗にする。
  - 結果: 手元の `sqlite3` 3.46.1 で、両バージョンとも行数と値が一致した。CI（`ubuntu-latest`。Ubuntu 26.04）の `sqlite3` も 3.46.1 で、同じ 2 つのテストが通った（PR の CI のログで確認）。さらに Docker の `ubuntu:14.04`（`sqlite3` 3.8.2）に、`sample_main` の出力を流して、エラーなしで全表が復元できた。`ubuntu:12.04`（3.7.9）は、データファイルの `INSERT` が `near ",": syntax error` で失敗した。
  - 出力が使う構文のうち新しい版で入ったもの: 複数行の `INSERT ... VALUES (...),(...)`（3.7.11）、`char()`（3.7.16。改行を含む文字列。SQLite のリリースノートによる。3.7.16 そのものでは未確認）。それ以外（`PRAGMA foreign_keys`、`BEGIN` / `COMMIT`、`X'..'`、`CAST`、`DESC` つきのインデックス、`ON DELETE` / `ON UPDATE`、`DEFAULT CURRENT_TIMESTAMP`）はこれより古い版から使える。**最低の版は 3.7.16**（実機で通ったのは 3.8.2 から）。
  - 確認していないこと: 3.7.16〜3.8.1 の実機。古い版では 1 文の長さの上限（`SQLITE_MAX_SQL_LENGTH`）の既定が 1,000,000 バイトで、`INSERT` の上限の既定（`MaxInsertBytes` = 1048576）より小さい版がありうる。3.8.2 での動作は確かめていない（3.46.1 は 1,000,000,000）。
- 【確認済み】`--target postgres` の出力を PostgreSQL 14〜18 に復元できること（B-068。確認日: 2026-11-12。出力は `mysql:8.4` から取った）。
  - 方法: `task test:integration:postgres`（`PG_VERSIONS` で版を選べる。既定は `14 15 16 17 18`。`TESTFLAGS` の既定は `-run Postgres`）。版ごとに MySQL 8.4 と PostgreSQL のコンテナを起動し、`TestPostgres*`（`internal/dump/postgres_test.go`。出力を実機に復元して MySQL と行数・値を比べる）を実行する。`task test:integration` の既定は `postgres:17` のまま変えていない（`PG_IMAGE` で差し替えられる）。
  - 結果: 5 つの版すべてで、6 つの統合テスト（`TestPostgresRoundTrip`、`TestPostgresDeterministicAndMinimalDiff`、`TestPostgresSplitsLargeTable`、`TestPostgresThenMySQLDumpRemovesTheConstraintsFile`、`TestPostgresRefusesValuesItCannotHold`、`TestPostgresBooleanRuleOnThisServer`）が通った。通らない版はなかった。確かめた版は 14.24、15.19、16.15、17.11、18.6（Docker の `postgres:14`〜`postgres:18`）。MariaDB と 5.7 の出力を使う 2 つのテストは、この実行では skip される（元のサーバーが違うため）。
  - 最低の版: 出力が通る版は **14 以上**（実機で確かめた範囲）。13 以前は確かめていない。README に版の制限は書かない（通らない版がないため）。
- 【確認済み】文字列キーの `ORDER BY CAST(col AS BINARY)` は filesort になるが、影響は小さい（B-018。確認日: 2026-11-12。`mysql:8.4`（8.4.x）で 10 万行と 100 万行、`mysql:8.0` で 10 万行）。コードは変えていない。
  - 方法: `task sample:big ROWSIZE=short` の `sample_big.str_short`（`k CHAR(32)` の主キー。行番号の MD5）と `int_short`（`BIGINT` の主キー）。`SELECT k, a, b FROM ... ORDER BY ...` を `mysql --quick` で全行読み、3 回の中央値を取った（`docker exec` を含む壁時計。クライアントとサーバーは同じホスト）。`EXPLAIN` と `Sort_*` のステータスは同じセッションで取った。`mysqldance` 全体の時間は、`int_short` と `str_short` を別々の DB に分けて 3 回ずつ測った。
  - `EXPLAIN`: `ORDER BY CAST(k AS BINARY)` は `type=ALL`、`key=NULL`、`Extra=Using filesort`。素の `ORDER BY k` は `type=index`、`key=PRIMARY`、`Extra` なし。
  - 所要時間（`SELECT` の全行読み、秒）:

    | 問い合わせ | 8.4 / 10 万行 | 8.4 / 100 万行 | 8.0 / 10 万行 |
    |---|---|---|---|
    | 整数キー `ORDER BY id`（`index`） | 0.07 | 0.31 | 0.06 |
    | 文字列キー `ORDER BY k`（`index`。照合順序の順なので**使えない**。比較用） | 0.09 | 0.55 | 0.08 |
    | 文字列キー `ORDER BY CAST(k AS BINARY)`（**現在**。filesort） | 0.11 | 0.71 | 0.12 |
    | 文字列キー `ORDER BY k COLLATE utf8mb4_bin`（filesort） | 0.10 | 0.73 | 0.11 |
    | 列が `utf8mb4_0900_bin` で `ORDER BY k`（`index`） | 0.08 | 0.51 | 0.09 |

    filesort の増分は、100 万行で 0.16〜0.2 秒（約 30%）。`Sort_merge_passes` は 10 万行で 7、100 万行で 66（既定の `sort_buffer_size`。`Created_tmp_disk_tables` は 0）。ディスクとメモリの使用量は測っていない。`mysqldance` 全体は、100 万行（`short`）で整数キー 0.57 秒、文字列キー 1.03 秒（行の中身も違うので、差の全部が `ORDER BY` ではない）。
  - 注意（filesort は最初の行が出るのが遅い）: 並べ替えが終わるまで 1 行も返らない。`--read-timeout`（0 以外）を設定すると、巨大な文字列キーの表で並べ替えの間に切れうる（README の `--read-timeout` の記述にある）。この点は測っていない。
  - 代替案: (1) `COLLATE utf8mb4_bin` で並べる: 列の照合順序と違うのでインデックスが使えず、改善しない。(2) 列がもともとバイト順の **NO PAD** の照合順序（`utf8mb4_0900_bin`、`binary` / `varbinary`）なら、`CAST` を省くとインデックスで返る。**PAD SPACE の `*_bin`（`utf8mb4_bin`、`latin1_bin` など）は不可**: 8.4 で `'a'`、`'a\t'`、`'a!'` を `utf8mb4_bin` で並べると `61`、`6109`、`6121` ではなく `6109`、`61`、`6121` になる（末尾を空白で埋めて比べるため）。決定性（バイト順）を壊すので、省いてよいのは NO PAD のものだけ。(3) 影響が小さく、効くのは NO PAD の `_bin` の列だけなので、入れない。必要になったら、列の照合順序の判定を足す別項目にする。
  - 確認していないこと: 5.7 / 5.6 / MariaDB、複数列の文字列キー、`utf8mb4` 以外の文字セット、行が大きい表（`large`）、サーバーの `sort_buffer_size` を変えたとき、ディスクとメモリの使用量。
- 対象バージョンは MySQL 8.0 系以降で決定済み（10.5 節）。MariaDB は B-037 で差異を確認済み（上）、B-038 で対応した（`dialect` に、セッション設定・ビュー依存の取得・既定値の読み方を持たせた。ビュー依存は `VIEW_DEFINITION` の `` `schema`.`name` `` を大文字小文字を区別せずに探す）。5.7 系は B-039 で差異を確認した（上）。対応は B-040。5.6 は B-079 で差異を確認した（上）。B-095 で対応した（`dialectFor` が 5.6 を `mysql57` にする。`dialect` の中身は変えていない。専用の統合テストは `task test:integration:mysql56`）。

## 12. バックログ（MVP の範囲外。後で課題として積む）

- **MyISAM など非トランザクションテーブルの一貫性**: MVP は警告して続行する。将来は `LOCK TABLES ... READ` や `FLUSH TABLES WITH READ LOCK` による取得を検討する。古いデータを持ってくる話と合わせて扱う。
- **MariaDB 対応**: 「バージョン差」ではなく「DBMS の方言差」として扱う。バージョン依存の処理を1か所にまとめる方針（10.5 節）を、方言差にも広げる。主な差異: `SHOW CREATE TABLE` の既定値の書式（`current_timestamp()`）、照合順序（`utf8mb4_uca1400_ai_ci` 等）、JSON が `LONGTEXT` + `CHECK`、シーケンス・システムバージョニングテーブル・`UUID`/`INET6` 型、`VIEW_TABLE_USAGE` がない（ビュー依存解決の代替が必要）、認証プラグイン、`sql_mode`。このうち `sql_mode` 以外の差異は B-037 で実機確認済み（11 節）。認証は `root` のパスワード認証が通ることだけ確認した。`COLUMN_DEFAULT` の読み方など、記憶になかった差異も見つかった。
- **MySQL 5.7 系など古いバージョンの対応**: 5.7 の差異は B-039 で実機確認済み（11 節）。実装は B-040。5.6 以前は対象外。
- **並列取得**（スナップショットを複数接続で揃える方法を含む）。【実装】B-042（`--jobs`。6.1 節）。出力が並列度によらないことのテストは B-043。
- **PostgreSQL / SQLite 向け出力**（boolean 変換、外部キーを後付けする順序など）。
  - 【実装】SQLite は B-036 で対応した（`--target sqlite`）。ライタは `internal/writer` の `SQLite`。テーブル定義は `SHOW CREATE TABLE` を解析せず、`information_schema` から構造（`source.TableDef`）で読んで組み立てる。構造が要るライタだけが `Writer.NeedsDefs()` で `true` を返し、`dump` がその時だけ `Snapshot.Defs` を呼ぶ。MySQL の出力は変わらない。写せないもの（ビュー、生成列、`ON UPDATE`、式の既定値、FULLTEXT / SPATIAL / 式のインデックス、CHECK、他 DB への外部キー）は警告して出力しない。値の正確さを優先し、`DECIMAL` / `BIGINT UNSIGNED` / `BIT(64)` は `TEXT` にして文字列で書く（SQLite が桁あふれを `REAL` にして値を変えるため）。1 レコード 1 行を保つため、文字列中の改行は `'a'||char(10)||'b'` と書く。ビューの変換は B-062。PostgreSQL は B-035 で、同じ `--target` に値を足す。
- **巨大テーブルの主キー範囲による分割出力**（ファイル名末尾に番号を足す）。
- **大規模データ向けのストリーム処理**。【実装】キーのあるテーブルは B-044 で対応した。`source.EachRow` が行を 1 行ずつ渡し、`Writer.NewRowWriter` が行を受けて、ファイルが要るたびに dump の `open` を呼ぶ（`datafile.Stream` が INSERT とファイルの区切りを行の内容だけで決める）。`--rows-per-file` のときの部品の名前は総数に依存するので、一時ファイルに書き、テーブルの終わりでリネーム先を決める。出力はバイト単位で変わらない。キーなしテーブルのソートは B-045。
- **トリガー、ストアドルーチン、イベントのダンプ**。【実装】トリガーは B-046 で対応した（ルーチンは B-047、イベントは B-048）。
  - ファイル名は `<db>.20-triggers.sql`。辞書順でデータの後・制約ファイル（`90`）の前になるので、トリガーはロードの後に作られ、ロード中に動かない。トリガーのない DB はこのファイルを作らず、前回のファイルは古いファイルとして消える。
  - 【判断】`DEFINER=...` はビューと同じ理由で除く。作成時の `sql_mode` はトリガーごとに `SET SQL_MODE` で残す（本体の解釈が変わるため）。接続の文字コードは残さない（固定ヘッダーの `utf8mb4`）。
  - 【判断】本体に `;` があるので、`DELIMITER`（`$$`。文に含まれる、または文の末尾の文字と結びつくなら、次の `//`、`##`、`%%`、`!!`、`$$$`、…）で区切る。`DELIMITER` で始まる行がある本体はエラーにする（クライアントがコマンドとして実行するため）。`DELIMITER` は `mysql` クライアントのコマンドで、`docker-entrypoint-initdb.d` はクライアントで流すので通る。
  - 順序: テーブル → タイミング → イベント → 実行順（`ACTION_ORDER`）→ 名前。同じテーブル・タイミング・イベントのトリガーは実行順に作るので、復元後も同じ順に動く。
  - MySQL 以外のターゲット（SQLite、PostgreSQL）は本体が MySQL の構文なので出力せず、トリガーごとに警告する。
- **出力のアトミック化**: 一時的な名前で書いてからリネームし、ダンプが途中で失敗しても出力先に不完全な DB が残らないようにする（MVP は失敗時に不完全になりうることを README に明記している）。
- **差し替え可能なライタ**: 8 節は一部しか実現していない。型付きのレコード値は入った（B-033）が、ライタは MySQL 向けの `datafile` / `schemafile` だけ。他 DBMS 出力の前に、ライタのインターフェースを入れる（B-034）。
- **読み出し元の検査機能**: 13 節に案を書いた（B-078。コードは変えていない）。実装は、案を見てから別項目にする。
- **`latin1` の列に入った UTF-8**: 14 節に案を書いた（B-081。コードは変えていない）。実装は、案を見てから別項目にする。
- **ダンプの差分の要約**: 15 節に案を書いた（B-083。コードは変えていない）。実装は、案を見てから別項目にする。

## 13. 読み出し元の検査機能の設計案（B-078）

この節は**案**であり、コードは変えていない。接続先のサーバーを前もって調べ、「このオプションで読める」「この設定は要注意」を、ダンプを始める前に報告する機能（`mysqldance check`）の形を決める。

### 13.1 位置づけ

- 検査するのは**読み出し元だけ**。本ツールが接続できるのは読み出し元だけで、復元先には接続しない（13.8）。
- ダンプが今エラーや警告にしていることを、**ダンプの前に、まとめて**示すのが主な価値である。ダンプは最初の 1 つで止まる（例: 権限のないビューが 3 つあっても 1 つ目で止まる）が、検査は全部を列挙する。
- ダンプの動作は変えない。`check` は別のサブコマンドで、既存のコマンドライン（`mysqldance [flags] -o DIR DB...`）は変わらない。

### 13.2 実機で確かめたこと

確認日: 2026-11-12。`mysql:8.4`（8.4.13）、`mysql:5.7.44`、`mysql:5.6`（5.6.51）、`mariadb:10.11`（10.11.19）。5.6 と 5.7.44 は `--platform linux/amd64`（エミュレーション）。検査の問い合わせを `mysql` クライアントで流して確かめた。コードは変えていない。

- **権限の読み方**:
  - 8.4 で、ロール経由で `SELECT, RELOAD, SHOW VIEW` を持つユーザー（`SET DEFAULT ROLE` 済み）に対して、`SHOW GRANTS FOR CURRENT_USER()` は有効なロールの権限をまとめた行（`GRANT SELECT, RELOAD, SHOW VIEW ON *.* TO ...`）を返した。`information_schema.USER_PRIVILEGES` は、同じユーザーの行が `USAGE` だけで、**ロール経由の権限を含まない**。つまり `USER_PRIVILEGES` で `RELOAD` の有無を調べると、ロールで権限を持つアカウントを誤って「なし」と判定する。権限は `SHOW GRANTS FOR CURRENT_USER()` で読む。
  - `USER_PRIVILEGES` は、8.4 では**他のユーザーの行も返す**（`GRANTEE` で自分に絞る必要がある）。
  - `SHOW GRANTS` の書式は版で違う: 5.6 と 5.7.44 は `GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' ...`（単引用符）、8.4 と MariaDB は `` `root`@`%` ``（逆引用符）。5.6 と MariaDB の `IDENTIFIED BY PASSWORD '*...'`（ハッシュ）が行に入る。MariaDB は `GRANT PROXY ...` の行も返す。**行の全文をログに出してはならない**（ハッシュが入る）。権限名だけを取り出す。
  - 列単位の権限は、`information_schema.COLUMN_PRIVILEGES`（8.4）と `SHOW GRANTS` の `GRANT SELECT (`id`) ON `probe`.`inno` ...` に出る。このとき `SHOW CREATE TABLE` は全列を出す一方で、`information_schema.COLUMNS` は権限のある列だけ（既存の `checkAllColumnsVisible` が使う差）。
- **`FLUSH TABLES WITH READ LOCK`（FTWRL）の権限**: 8.4 で、`RELOAD` を持たず `FLUSH_TABLES`（動的権限）だけを持つユーザーで FTWRL が**通った**。`BACKUP_ADMIN` だけでは通らず、`Error 1227: Access denied; you need (at least one of) the RELOAD or FLUSH_TABLES privilege(s) for this operation` になった。現在のエラー文言（`source.go` の「requires the RELOAD privilege」）と README の「Required privilege: RELOAD」は `FLUSH_TABLES` を言っていない（8.0 は未確認）。FTWRL は他のセッションの書き込みを止めるので、**権限を調べるためにロックを取ってはならない**。権限は `SHOW GRANTS` から読む。
- **`SHOW VIEW`**: 8.4 で、`SELECT` だけを持つユーザーは `information_schema.VIEWS.VIEW_DEFINITION` が空（長さ 0。`NULL` ではない）で、`SHOW CREATE VIEW` は `Error 1142: SHOW VIEW command denied` になる。既存の `viewDefinitions`（5.7 と MariaDB）は空の定義をエラーにする。8.x の `viewTableUsage` は定義を読まないので、**`SHOW CREATE VIEW` の段で初めて失敗する**。検査は全ビューに `SHOW CREATE VIEW` を実行して、失敗したビューを列挙する。
- **サーバーの変数**（`@@GLOBAL.`。読めない変数は `Error 1193: Unknown system variable` になる）:

  | 変数 | 8.4.13 | 5.7.44 | 5.6.51 | MariaDB 10.11.19 |
  | --- | --- | --- | --- | --- |
  | `show_create_table_verbosity` | あり（ダンプの `SET SESSION` が通る。グローバルの値は未確認） | あり（`0`） | **なし** | **なし** |
  | `explicit_defaults_for_timestamp` | 未確認 | `0` | `0` | `1` |
  | `lower_case_table_names` | 未確認 | `0` | `0` | `0` |
  | `sql_mode`（既定） | 未確認 | 厳格（`NO_ZERO_DATE` など） | `NO_ENGINE_SUBSTITUTION` だけ | 厳格（`NO_ZERO_DATE` なし） |
  | `character_set_server` | 未確認 | `latin1` | `latin1` | `utf8mb4` |
  | `have_ssl` | **なし（`Unknown system variable`）** | `YES` | `DISABLED` | `DISABLED` |
  | `tls_version` | `TLSv1.2,TLSv1.3` | `TLSv1,TLSv1.1,TLSv1.2` | **なし** | `TLSv1.2,TLSv1.3` |
  | `require_secure_transport` | あり（`0`） | あり | **なし** | あり |

  8.4 の `have_ssl` が無いので、「サーバーが TLS を持つか」は変数ではなく、**検査自身のセッションの `SHOW SESSION STATUS LIKE 'Ssl_version'` / `'Ssl_cipher'`** で判定する（TCP の 8.4 で `TLSv1.3` / `TLS_AES_128_GCM_SHA256`。Unix ソケットでは空）。
- **値の検出**（`probe` DB に、ゼロ日付、NUL を含む文字列、`TINYINT(1)` の `2`、`TIME` の `838:59:59`、`latin1` の列に入れた UTF-8 のバイト列を持つ表を作って確かめた。8.4）:
  - ゼロ日付: `d = '0000-00-00'` は、**厳格モードのセッションでは `Error 1525: Incorrect DATE value` で失敗する**。セッションを `sql_mode = ''` にすると通る（本ツールのダンプ接続は `sql_mode = ''` に固定している。10.5 節）。`d < '0001-01-01'` は厳格でも通る。`0000-00-10` のような月か日が 0 の値は `DAY(d) = 0` / `MONTH(d) = 0` で検出できた（2 行）。`DATETIME` も同じ（1 行）。
  - NUL: `LOCATE(CHAR(0), col) > 0` で検出できた（1 行）。
  - `TINYINT(1)` が 0 / 1 でない値: `col NOT IN (0, 1)` で検出できた（1 行）。
  - `TIME`: PostgreSQL 向けの出力は `interval` にするので（`writer/postgres.go`）、MySQL の範囲（`-838:59:59` から `838:59:59`）は入る。**検査の対象にしない**。
  - `latin1` の列: `s <> CONVERT(s USING ascii)` で、非 ASCII を含む行を数えられた（1 行）。`HEX(s)` は `E38182` で、**列の中のバイト列は UTF-8 のまま**である。UTF-8 かどうかの判定は B-081 の範囲（13.4 の C12）。

### 13.3 コマンドの形

```
mysqldance check [接続フラグ] [--target mysql|sqlite|postgres] [--format table|json]
                 [--no-scan] [--fail-on warn|error] [--no-lock] [--jobs N] DB [DB...]
```

- **接続フラグ**（`-h` `-P` `-u` `--socket` `--password-file` `--ssl-mode` `--ssl-ca`）、`--log-level`、`--log-format`、対象 DB は、ダンプと**同じ**。出力ディレクトリ（`-o`）は要らない。ファイルを作らない。
- `--target`、`--no-lock`、`--jobs` は、**ダンプで使う予定のオプション**を渡すためにある。検査は、そのオプションでダンプしたときの結果を答える（例: `--jobs 4` で `RELOAD` がなければエラー。`--no-lock` で非 InnoDB があれば警告）。渡さなければダンプの既定と同じ（`--target mysql`、ロックあり、`--jobs 1`）。
- 出力ディレクトリの検査（既存ファイルが通常のファイルか、ファイル名の長さと大文字小文字の衝突）は、`-o` が要るので、`check -o DIR` を渡したときだけ行う（C15）。
- `--no-scan`: 値の検出（C11 と C12）をしない。表の全行を読むのでダンプと同程度の時間がかかる。既定は行う（前もって示すのがこの機能の目的）。

### 13.4 検査する項目

重さの印: **構造** = カタログの問い合わせだけ。**値** = 表の全行を読む。「共有」は 13.7 の、ダンプと判定のコードを共有する先。

| ID | 何を調べるか | 見つかったとき（重さ。重大度） |
| --- | --- | --- |
| C01 版と方言 | `SELECT VERSION()` を `dialectFor` に渡す（ダンプと同じ関数）。方言（`mysql8` / `mysql57` / `mariadb`）と、確認した版かを報告する | 拒否される版: **error**（メッセージは `dialectFor` のもの。5.6 以前は B-095 と B-080 の後に変わる）。確認した版以外（5.7.44 以外の 5.7、5.6.51 以外の 5.6、10.11 と 11.4 以外の MariaDB）: **info**（「README の Compatibility で未確認」）。方言ごとの注意（5.7 の `int(11)` と `latin1`、MariaDB の照合順序）は **info** |
| C02 TLS | 検査自身の接続の `Ssl_version` / `Ssl_cipher`（構造）と、`--ssl-mode` の要求。変数は読める版だけ読む: `have_ssl`（8.4 には無い）、`tls_version`（5.6 には無い）、`require_secure_transport` | `--ssl-mode preferred`（既定）で、サーバーが TLS を使えず**平文になった**: **warn**（「パスワードと全行が平文で流れた。`--ssl-mode required` を付けると失敗にできる」）。接続が TLS のハンドシェイクで失敗した: **error**（C02 の診断。13.5）。`tls_version` が TLS 1.2 を含まない（`TLSv1,TLSv1.1`）: **warn**（「本ツールの下限は TLS 1.2。`--ssl-mode disabled` では平文になる。B-082 の結果を待つ」）。`require_secure_transport = ON` なのに `--ssl-mode disabled`: 接続が失敗するので C02 の診断になる |
| C03 ロック権限 | `SHOW GRANTS FOR CURRENT_USER()` から、`*.*` の `RELOAD` / `FLUSH_TABLES`（8.x）/ `ALL PRIVILEGES` を探す（構造）。判定できないとき（行の書式が読めない）は「不明」 | `--jobs` が 2 以上で権限なし: **error**（推奨: `--jobs 1`）。非 InnoDB があり（C06）、`--no-lock` なしで、権限なし: **error**（推奨: 権限を付ける、または `--no-lock`（一貫性は保証されない））。InnoDB だけで `--jobs 1`: 権限は要らない（**ok**）。不明: **warn**（「権限を判定できなかった。ロックを取って確かめることはしない」） |
| C04 `SHOW VIEW` | 全ビューに `SHOW CREATE VIEW` を実行する（読み取りだけ。構造） | 失敗（`Error 1142`）したビューを全部挙げる: **error**。推奨: 該当ビューに `SHOW VIEW` を付ける |
| C05 全列の `SELECT` と `TRIGGER` | 各表の `SHOW CREATE TABLE` の列と `information_schema.COLUMNS` の列を比べる（`checkAllColumnsVisible`）。トリガーのある DB では、`SHOW CREATE TRIGGER` が文を返すかを見る | 読めない列がある表を全部挙げる: **error**（推奨: 列の権限を付ける。ダンプはその表を部分的に出さずに止まる）。`TRIGGER` 権限がない: **error** |
| C06 非 InnoDB | `information_schema.TABLES` の `ENGINE`（構造）。`NULL` はビュー | 非 InnoDB の表を挙げる: **info**（ロックが要る旨。C03 と組む）。`--no-lock` が指定されていれば **warn**（「その表は一貫性の範囲の外」。ダンプの警告と同じ） |
| C07 対応しない `TABLE_TYPE` | `BASE TABLE` と `VIEW` 以外（MariaDB の `SEQUENCE` / `SYSTEM VERSIONED`。`checkTableType`） | 全部挙げる: **error**（ダンプは最初の 1 つで止まる。推奨: 対象の DB から外す。出力できない） |
| C08 `show_create_table_verbosity` | `@@GLOBAL.show_create_table_verbosity`（8.0 系と 5.7.22 以降。変数がなければ**この項目を出さない**） | 8.x: ダンプは接続のセッションで 0 に固定するので影響しない: **ok**。5.7 は固定を送らない（R-069。版を見分けないため）ので、グローバルが `ON` なら `SHOW CREATE TABLE` が変わる: **warn**（「サーバー側を `OFF` にする。README に書いた」）。5.7.21 以前と 5.6 と MariaDB: この変数がないので **ok** |
| C09 ゼロ日付とその素地 | `@@GLOBAL.explicit_defaults_for_timestamp`、`@@GLOBAL.sql_mode`（構造）。表の `COLUMN_DEFAULT` が `0000-00-00` で始まるもの（構造）。値の検査: `DATE` / `DATETIME` / `TIMESTAMP` の列で `col < '0001-01-01'` または `DAY(col) = 0` または `MONTH(col) = 0`（値。セッションは `sql_mode = ''`。13.2） | 値がある: 列ごとに件数を出す。`--target postgres`: **error**（ダンプが `is not a date PostgreSQL accepts` で止まる。推奨: `--target mysql` か `sqlite`、またはデータを直す）。`--target mysql` / `sqlite`: **info**（復元はヘッダーの `SET SQL_MODE` で通る。B-079）。`explicit_defaults_for_timestamp = OFF` かつ `sql_mode` に `NO_ZERO_DATE` がない（5.6 の既定）: **info**（「暗黙の `TIMESTAMP` の `DEFAULT '0000-00-00 00:00:00'` が出る。`--target postgres` は既定値を出さずに警告する」） |
| C10 `lower_case_table_names` | `@@GLOBAL.lower_case_table_names`（構造） | `1` または `2`: **info**（「表名は小文字、または保存された名前のまま比較される」。復元先の設定が違うと、名前の大文字小文字が合わなくなりうる）。`0` で、大文字小文字だけ違う名前の表がある（出力ファイル名が大文字小文字で衝突する）: **error**（ダンプの `output file ... collides case-insensitively` と同じ判定。C15） |
| C11 `--target` に入らない値 | `--target postgres`: NUL を含む文字列（`LOCATE(CHAR(0), col) > 0`）、`TINYINT(1)` が 0 / 1 以外（`col NOT IN (0, 1)`）、`BIT(n)` の幅に収まらない値、ゼロ日付（C09）。`--target sqlite`: 値で入らないものは**ない**（`TEXT` にする。構造で写せないもの — ビュー、生成列、式の既定値など — はダンプの警告と同じものを C11 に出す）。`--target mysql`: なし。13.7 の方式で、実際のライタの変換に通して判定する | 件数を、表と列ごとに出す: **error**（ダンプがその値で止まる）。推奨: `--target` を変える、またはデータを直す。構造の警告（`sqlite` / `postgres` が写さないもの）: **warn** |
| C12 `latin1` の列の UTF-8 | 文字セットが `latin1`（と、その別名）の列で、非 ASCII を含むものを数える（値。13.2 の `s <> CONVERT(s USING ascii)`）。UTF-8 のバイト列かどうかの判定は、14.4 で決めた集計問い合わせ（B-081）で行う。検出して推奨するが、ダンプは自動では切り替えない（14.3） | 14.4 の 4 つの分類で報告する: 全部が有効な UTF-8 は **warn**（`--latin1-as-utf8` を推奨）、混在は **warn**（`--latin1-as-utf8` はエラーになる）、全部が不正は **info**。非 ASCII がなければ何も出さない。この項目の実装は、14.7 の 4 |
| C13 規模 | `TABLE_ROWS` の推定行数が `largeTableRows`（50 万行）を超え、キーがない表（構造） | **warn**（ダンプの警告と同じ文言） |
| C14 対象 DB | 指定した DB の存在、システム DB（`checkNotSystem` / `checkDatabases`） | 存在しない、またはシステム DB: **error** |
| C15 出力ファイル名 | 表名と DB 名から作るファイル名の長さ（255 バイト）と、大文字小文字の衝突。`-o` を渡したとき、既存の出力ディレクトリが通常のファイルだけか（`dump.go` の判定） | **error**（ダンプが書き出しの前に失敗する判定と同じ） |
| C16 文字セットと照合順序 | 使われている `CHARACTER_SET_NAME` / `COLLATION_NAME` の一覧（構造） | **info**（事実の列挙だけ。どの復元先に入るかは README の表で示す。13.8） |

重大度の意味（`--format json` の `severity`）:

- **error**: このオプションでダンプすると失敗する、または検出した問題で出力が不完全になる。
- **warn**: ダンプは通るが、一貫性や互換性の保証が弱い。
- **info**: 事実と、あれば推奨。
- **ok**: 検査して問題がなかった。
- **unknown**: 権限が足りないなどで、判定できなかった（C03 の書式、読めない変数）。

### 13.5 出力の形と終了コード

- **人が読む表**（`--format table`。既定）: 標準出力に、項目 ID ごとの 1 行（`ok` / `warn` / `error` / `info` / `unknown`、対象、メッセージ）と、末尾に**推奨するオプションの一覧**（例: `--ssl-mode required`、`--jobs 1`）。ログ（`--log-level` / `--log-format`）は今まで通り標準エラー出力で、標準出力は結果だけにする（`--version` と同じ流儀）。
- **機械が読める形式**（`--format json`）: 1 行 1 検査結果の JSON Lines。各行は `{"id":"C03","severity":"error","scope":"server","object":"","message":"...","recommend":["--jobs 1"]}`。`scope` は `server` / `database` / `table` / `column`、`object` は DB・表・列の名前。
- **決定性**: 結果は ID、DB、表、列の順に並べる。時刻や接続のスレッド ID などを入れない（出力を git で管理しても、サーバーが変わらなければ同じになる）。
- **メッセージ**: ダンプのエラーと同じ文言を使う（13.7）。パスワードと `SHOW GRANTS` の行の全文（ハッシュが入る）は出さない。
- **終了コード**（`check` のみ。既存のダンプは 0 と 1 のまま）:

  | コード | 意味 |
  | --- | --- |
  | 0 | `--fail-on`（既定は `error`）以上の結果がない |
  | 1 | `--fail-on` 以上の結果がある（`error` が 1 つでもあれば、そのオプションでのダンプは失敗する。`--fail-on warn` では警告でも 1） |
  | 2 | 検査を実行できなかった（フラグの誤り、接続と認証の失敗、検査の途中の失敗） |

  CI で「ダンプの前に検査して、1 なら止める」使い方を想定している。ダンプの失敗（1）と検査の実行失敗（2）を分けるのは、スクリプトが「接続できない」と「問題を見つけた」を区別できるようにするため。
- **接続できないとき**: 検査は最初の接続から始まるので、接続の失敗は結果の 1 行（`C02` または `C01`）になる。TLS のハンドシェイクの失敗（`tls: protocol version not supported` など）は C02 の **error** にし、TLS 1.2 未満しか話さないサーバー向けの推奨（B-082 の結果が出るまでは `--ssl-mode disabled` が平文であることを明記）を付ける。**平文に黙って落とさない**（再試行しない。R-006 と同じ）。認証の失敗は C01 の前の、検査を実行できなかったもの（終了コード 2）。

### 13.6 検査が読み取りだけで、サーバーの状態を変えない保証

検査は、次の 4 つで保証する。

1. **許可する文を限る**: 検査の接続が送る文は、`SELECT`、`SHOW`、`SET SESSION`（セッションだけの変数。ダンプの `pinSession` と同じもの）、`START TRANSACTION READ ONLY`、`ROLLBACK` だけ。`FLUSH`、`LOCK TABLES`、`UNLOCK TABLES`、`SET GLOBAL`、`INSERT` / `UPDATE` / `DELETE` / DDL は送らない。**ロックを取らない**: `FLUSH TABLES WITH READ LOCK` は権限の確認にも使わない（13.2。他のセッションの書き込みを止めるため）。権限は `SHOW GRANTS` で読む。
2. **サーバーに書き込みを拒否させる**: 検査の接続は `START TRANSACTION READ ONLY` で始める。書き込みの文があれば、サーバーが拒否する。`START TRANSACTION READ ONLY` が 5.6 と MariaDB で通るかは、**この調査では確かめていない**（マニュアルでは 5.6.5 以降）。実装の項目で確かめる。通らない版では、このトランザクションを省き、1 と 3 で保証する。
3. **テストで文を検査する**: ドライバの前に文を記録するラッパー（`database/sql/driver` の `Connector` を包む）を置き、検査が送った**全ての文の先頭の語**が、1 の許可リストにあることを、統合テストで確かめる。MySQL なしの単体テストでは、ラッパーに偽のドライバをつないで同じ検査をする。
4. **サーバー側のカウンタで確かめる**: 検査の終わりに、同じセッションの `SHOW SESSION STATUS LIKE 'Com_%'` のうち、`Com_flush`、`Com_lock_tables`、`Com_insert`、`Com_update`、`Com_delete`、`Com_create_*`、`Com_drop_*`、`Com_alter_*`、`Com_grant` などが 0 であることを、統合テストで確かめる（ラッパーが見落とした文を、サーバーの数え方で捕まえる）。

さらに、**統合テストは `SELECT` と、必要な `SHOW VIEW` だけを持つ読み取り専用のアカウント**で `check` を通す（そのアカウントで検査が最後まで走ることが、書き込みの権限を使っていない証拠になる）。ダンプの特権テスト（R-073）と同じように、アカウントは DB 名ではなく固定の短い名前で作る。

値の検査（C09、C11、C12）は表の全行を読むが、読み取りだけで、ダンプと同じく `READ ONLY` のトランザクションの一貫した読み取り（InnoDB）で行う。ロックは取らないので、非 InnoDB の表は検査中の更新が見えうる（検査は目安であって、一貫性の保証は求めない）。

### 13.7 ダンプとの判定の共有

**原則: 判定は 1 か所に置き、ダンプと検査の両方がそれを呼ぶ。** 検査だけの判定を別に書くと、ダンプが失敗するのに検査は通る、またはその逆になる（決定性の考え方と同じく、2 つの実装は必ずずれる）。

方式: 今は判定がその場で `error` を返す（`checkTableType`、`checkAllColumnsVisible` など）。これを、**結果の列（`Finding`: ID、重大度、対象、メッセージ、推奨）を返す関数**に分け、ダンプは「`error` が 1 つでもあれば、その先頭のメッセージのエラーを返す」薄い包みにする。検査は全部を集めて表示する。メッセージ本文は変えないので、ダンプのエラーメッセージと既存のテストは変わらない。

| 項目 | 今ある判定の場所 | 共有のしかた |
| --- | --- | --- |
| C01 | `dialect.go` の `dialectFor` | そのまま呼ぶ |
| 検査の接続の設定 | `source.go` の `pinSession`、`dialect.session` | 検査の接続も同じ設定を使う（読む値の見え方がダンプと同じになる） |
| C03 | `source.go` の `lockGlobal` のメッセージ（`RELOAD`）、`startParallel` の文言 | 権限の説明を定数にして、検査とダンプで同じ文を使う。`FLUSH_TABLES`（13.2）は別件（13.9） |
| C04 | `dialect.go` の `viewDefinitions`（空の定義）、`SHOW CREATE VIEW` | 全ビューを集める版を足し、ダンプは最初の 1 つを返す |
| C05 | `source.go` の `checkAllColumnsVisible`、`triggers.go` | 同上 |
| C06 | `source.go` の `firstNonInnoDB`、`Tables` の警告 | 「最初の 1 つ」を「全部」にした版を足す |
| C07 | `source.go` の `checkTableType` | 同上 |
| C10、C15 | `dump.go` のファイル名の長さと衝突の判定、`naming` | 検査から呼べる関数に出す（`-o` なしでもファイル名の判定だけ走らせる） |
| C11 | `sqlvalue.PostgresLiteral`、`writer/postgres.go` の名前と型の判定 | **行を読んで、ダンプと同じライタの変換に通す**（出力は捨てる）。SQL の述語（13.2 で確かめた `LOCATE(CHAR(0), ...)` など）は、速くするための前段として足せるが、判定の正はライタ |
| C13、C14 | `source.go` の `largeTableRows`、`checkNotSystem`、`checkDatabases` | そのまま呼ぶ |
| C02 | `tls.go` の `ValidateSSL`、`tlsConfig` | 同じ関数でフラグを検査する |

C11 を「ライタの変換に通す」方式にすると、検査の遅さの原因（全行を読む）と引き換えに、**ダンプが止まる値と検査が示す値が必ず一致する**。もう 1 つの案は、ダンプの `dump.Run` に `DryRun`（ファイルを書かない）を足して、検査の値の部分を「`DryRun` で `--target` を走らせる」にすることである。ダンプの全経路（書き出しの前の検査を含む）が検査に入るので判定は最も揃うが、`dump` の調停に「書かない」分岐が増える。**推奨は、前者（ライタの変換だけを呼ぶ）。** 後者は、C15 の判定をダンプと揃える必要が出た段階で足す。

### 13.8 復元先の検査を本ツールで扱わない理由と、README が示す範囲

本ツールは**復元先に接続しない**。理由は次のとおり。

- 出力はファイルで、復元は `mysql` クライアント、`docker-entrypoint-initdb.d`、`psql`、`sqlite3` が別に行う。復元先の版、設定（`sql_mode`、照合順序、`lower_case_table_names`）、既存のデータは、復元するときにだけ決まる。ダンプの時点に存在するとは限らない。
- 復元先の接続情報を持たせると、認証情報の扱い（保存先、ログ、権限）が増える。復元先が空とは限らず、`DROP TABLE IF EXISTS` を含むファイルを流す前に接続させるのは、誤った接続先への事故に近づく。
- 復元先の版の違いは、実機で確かめて**記録する**のが確実である（B-037 の MariaDB、B-039 の 5.7、B-079 の 5.6、B-068 の PostgreSQL）。検査の問い合わせで推定すると、README の表の「Tested」の基準（テストか設計書 11 節の記録の裏が取れている）を満たさない判定が出る。

代わりにやること:

1. **読み出し元の検査で、復元で困る値と構造を前もって示す**（C09、C11、C12、C16）。`--target postgres` で入らない値は、復元先の PostgreSQL に接続しなくても、読み出し元のデータとライタの規則から決まる。
2. **README の「Compatibility」の表（B-067）が、出力 × 復元先の版を示す**。この表が、復元先について本ツールが言える範囲の全てである。検査は、C01 の方言と C16 の照合順序の一覧で、利用者がこの表のどの行を見ればよいかを示す（例: 「ソースは MySQL 8.4。`utf8mb4_0900_ai_ci` を使っている」→ 表の「MariaDB には復元できない」を読む）。検査が表を内蔵して、「この組み合わせは失敗する」と断定することは、**この案には入れない**（表と内蔵のデータが 2 つの正になるため）。要るかどうかは、検査を使った結果を見てから別に決める。
3. 復元ファイルの先頭の固定ヘッダー（`sql_mode`、`FOREIGN_KEY_CHECKS` など。10.5 節）は、復元先の既定に依存しないために既にある。これも復元先を検査しないための設計である。

### 13.9 実装の分け方（案）と、別件

実装は、この案を見てから別項目にする（この項目では積まない）。分け方の案:

1. `Finding` と、ダンプの判定を「結果の列を返す関数 + 先頭を返す包み」に分ける。**動作は変えない**。ダンプの既存のテストが通ることが条件（13.7）。
2. `check` の骨組み: 接続、C01、C02、C14、表と JSON の出力、終了コード（13.5）。読み取りだけの保証（13.6）のテストを同時に入れる。
3. 構造の検査: C03 から C08、C10、C13、C16。
4. 値の検査: C09、C11（`--target postgres` と、構造の警告）、`--no-scan`。
5. C12 は B-081 の後。TLS の診断（C02 の下限の部分）は B-082 の後。5.6 を拒否しない（B-095）後は、C01 の方言の表に 5.6 を足す。
6. C15（`-o` つき）は、必要になったときに。

この調査で見つけた別件（コードの変更なので、この項目では扱わない）: FTWRL の権限の不足のメッセージと README が `RELOAD` だけを挙げていて、8.4 では `FLUSH_TABLES`（動的権限）でも通る（13.2）。B-104 に積んだ。

## 14. `latin1` の列に入った UTF-8 の扱いの設計案（B-081）

この節は**案**であり、コードは変えていない。`latin1` と宣言された列に、UTF-8 のバイト列がそのまま入っているデータ（古いシステムでは珍しくない）を、本ツールがどう扱うかを決める。

### 14.1 問題の形

古いアプリは、接続の文字セットを `latin1` にしたまま UTF-8 の文字列を送ることがある。サーバーは「`latin1` の文字が来た」と解釈して、**バイト列を変えずに**列に入れる。列の宣言は `latin1` だが、実体は UTF-8 である。本ツールは `SET NAMES utf8mb4` で読むので、サーバーは宣言どおりに `latin1` の各バイトを 1 文字として `utf8mb4` に変換して返す。`E3 81 82`（`あ`）は、3 文字の `ã`、U+0081、`‚` になり、ファイルには二重エンコードの文字列が書かれる。

ツールの動作は宣言に対しては正しい。間違っているのは宣言のほうである。そのため、検出も対処も「宣言と実体のずれを、利用者が知っていることを前提に、正す」形になる。

### 14.2 実機で確かめたこと

確認日: 2026-11-12。`mysql:8.4`（8.4.13）。`mysql` クライアントと、現在のバイナリ（`origin/main` の a4e3e77）で確かめた。5.7、5.6、MariaDB では**確かめていない**（14.8）。コードは変えていない。

テーブル（`latin1` の DB と表。`s`、`c`、`x` は `latin1` の `VARCHAR` / `CHAR` / `TEXT`、`u` は `utf8mb4`）に、バイト列を直接入れた（`0x..` の 16 進リテラルは、文字セット変換なしでそのまま列に入る）。

| id | 入れたバイト列（`s`） | 意味 |
| --- | --- | --- |
| 1 | `68656C6C6F` | ASCII |
| 2 | `E38182E38184E38186` | UTF-8 の `あいう` |
| 3 | `C3A9` | UTF-8 の `é` |
| 4 | `E9` | 本物の `latin1` の `é` |
| 5 | `F09F9880` | UTF-8 の 4 バイト文字（`😀`） |
| 6 | `61E38182E9` | UTF-8 と `latin1` が混ざった文字列 |
| 7 | `8093` | `cp1252` の `€` と `“`（UTF-8 としては不正） |
| 8 | `NULL` | |

**1. 現在のバイナリの出力と、復元の結果**

- `--target mysql` のデータファイルには、id 2 が `'ã‚ã„ã†'`（U+0081 を含む。以下も同じ）、id 3 が `'Ã©'`、id 5 が `'ðŸ˜€'` と書かれた。`u` 列（`utf8mb4`）は `'あいう'` のまま。ファイルは人間には読めないが、バイトは決定的である。
- **同じスキーマ（`latin1` の列）に復元すると、8 行全部がバイト単位で元と一致した**（`HEX` で比較）。ファイルの `utf8mb4` の文字を、復元先のサーバーが `latin1` に戻すとき、変換は元のバイトに戻る。バイト `0x01` から `0xFF` の 255 個を 1 つずつ入れた表（`cp1252` で未定義の `0x81`、`0x8D`、`0x8F`、`0x90`、`0x9D` を含む）でも、ダンプして復元した結果が一致した。**つまり、今の既定の出力は、`latin1` の列のまま復元する限り、壊れない**。
- 復元先の列が `utf8mb4` だと（スキーマを `utf8mb4` に書き換えて復元した）、二重エンコードのまま保存された。id 2 の `s` は `C3A3C281E2809AC3A3C281E2809EC3A3C281E280A0` になり、元の `E38182E38184E38186` ではない。id 5 は `C3B0C5B8CB9CE282AC`。本物の `latin1` の id 4 は `C3A9` で正しい。
- `--target sqlite` と `--target postgres` は、スキーマに文字セットを持たない（`TEXT` と `varchar(50)`）。同じ二重エンコードの文字列が、そのまま書かれた。**これらのターゲットでは、`latin1` のまま復元する道がない**ので、今の出力は壊れた文字になる。警告は出ない。

壊れ方をまとめると、**列を `latin1` のまま MySQL に戻すだけなら問題は起きず、`utf8mb4` に移す、SQLite か PostgreSQL に移す、ファイルを人が読む、のときに起きる**。

**2. バイト列を保つ読み方**（`SET NAMES utf8mb4` のセッション。サーバーが送るバイトを `od` で見た。id 2、4 の `s`）

| 読み方 | id 2 | id 4 |
| --- | --- | --- |
| 今（既定の読み） | `C3A3 C281 E2809A ...`（二重エンコード） | `C3A9` |
| `CAST(s AS BINARY)` | `E38182E38184E38186` | `E9` |
| `SET character_set_results = binary`、`s` | `E38182E38184E38186` | `E9` |
| `SET character_set_results = NULL`、`s` | 同上 | 同上 |
| `SET character_set_results = latin1`、`s` | 同上 | 同上 |
| `CONVERT(CAST(s AS BINARY) USING utf8mb4)` | `E38182E38184E38186` | **`NULL`**（警告 1300 `Invalid utf8mb4 character string: 'E9'`） |

- `character_set_results = binary` は、`utf8mb4` の列（`u`）も `utf8mb4` のバイトのまま返す（id 2 は `E38182...`、id 4 は `C3A9`）。`latin1` に変えると、`utf8mb4` の列の `あ` は `3F`（`?`）に化ける。**接続全体の設定でなく、列ごとの読み方を選ぶ必要がある**。
- `CAST(s AS BINARY)` が一番単純である。列ごとに選べて、サーバーの版に依存する関数を使わず、生のバイトが Go に届く。UTF-8 かどうかの判定は Go の `utf8.Valid` でできる。
- `CONVERT(... USING utf8mb4)` は、不正なバイト列で**エラーにならず `NULL` を返す**（警告だけ）。元の `NULL` と区別できないので、値の読み出しには使わない。検出の集計（14.4）では、`IS NULL` で数えるために使える。
- `ORDER BY` は今も `CAST(col AS BINARY)`（`orderByClause`）なので、並びは生のバイト順である。読み方を変えても、ファイルの行の順序は変わらない。

**3. 復元側で、正しい文字になる条件**

| 復元の形 | 結果（8.4） |
| --- | --- |
| `utf8mb4` の文字列 `'あいう'` を、`latin1` の列に、ファイルのヘッダー（`SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO'`。厳格モードでない）で入れる | **警告 1366 だけで、`3F3F3F`（`???`）になる。データが静かに失われる**。`STRICT_ALL_TABLES` を足すと `ERROR 1366` で止まる |
| `0xE38182...`（16 進リテラル）、`_binary 0x..`、`_latin1 0x..` を `latin1` の列に入れる | バイト列がそのまま入る |
| 列を `utf8mb4` にして `'あいう'` を入れる | `E38182E38184E38186`。正しい |
| 既存の表を `ALTER TABLE ... CONVERT TO CHARACTER SET utf8mb4` | **二重エンコードになる**（`C3A3C281...`）。サーバー側の直し方としては誤りである |
| 既存の表を `VARBINARY` にしてから `MODIFY ... utf8mb4` | `あいう`。正しい（バイトを付け替える） |

**一番大きいのは 1 行目である。文字列を `UTF-8` のテキストで書き出すなら、列の宣言も `utf8mb4` に変えなければならない**。宣言を `latin1` のまま、テキストだけ正しくすると、復元は静かにデータを壊す。ヘッダーの `sql_mode` が非厳格なためである（10.5 節で決めた固定のヘッダーを変えるのは、この問題の解ではない）。

**4. 列の宣言と同じ扱いになるもの**

`latin1` の列の `DEFAULT`、`COMMENT`、`ENUM` / `SET` のメンバーに UTF-8 のバイトを入れた表（`SET NAMES latin1` で作った）では、`SHOW CREATE TABLE`、`COLUMN_DEFAULT`、`COLUMN_COMMENT`、`COLUMN_TYPE` のすべてが、値と同じ二重エンコードの文字列（`'ã‚'`、`HEX` で `C3A3C281E2809A`）で返った。`latin1` のまま復元すれば戻る。`utf8mb4` に付け替えるなら、これらも付け替える必要がある。インデックスの接頭辞（`KEY k (s(5))`）は、`latin1` では 5 バイト、`utf8mb4` では 5 文字になる。

**5. 検出で分かる範囲**（14.4 の集計問い合わせを、上の表で流した）

```sql
SELECT COUNT(*) AS non_ascii,
       SUM(CONVERT(CAST(s AS BINARY) USING utf8mb4) IS NOT NULL) AS valid_utf8,
       SUM(CONVERT(CAST(s AS BINARY) USING utf8mb4) IS NULL) AS invalid
FROM b081.t WHERE s <> CONVERT(s USING ascii)
```

結果は `non_ascii = 6`、`valid_utf8 = 3`、`invalid = 3`（id 2、3、5 が有効、4、6、7 が不正）。NULL の行は `WHERE` で除かれる。

### 14.3 決めること

**【決定】既定は変えない。** 理由:

1. 既定の出力は、列を `latin1` のまま MySQL へ戻す限り、バイト単位で正しい（14.2 の 1。255 バイトの往復でも確認した）。
2. 既定を変えると、すでに git で管理している出力の全行が変わる。「差分の局所性」と、利用者の履歴を壊す。
3. UTF-8 かどうかを、ダンプが勝手に決めてはならない（14.4。決められない場合がある）。

**【決定】利用者が列を指定する、オプトインの `--latin1-as-utf8 SPEC` を足す案を推す。**

- `SPEC` は `DB.TABLE.COLUMN` の形で、各部分に `*` を使える（`*.*.*` は全部の `latin1` の列）。繰り返して指定できる。表の名前や列の名前に `.` が含まれるときは、指定できない（その場合は、別の指定の形が要る。実装の項目で決める。未決）。
- 指定は、その列を「`latin1` と宣言されているが、実体は UTF-8」とみなす宣言である。

指定された列の扱い:

- **読み**: `CAST(col AS BINARY)` で生のバイトを読む。Go で `utf8.Valid` を調べる。**不正なバイト列があればエラーにする**（表、列、行のキーの値（キーがなければ行の位置）を示す）。値ごとに「不正ならそのまま」とはしない。「黙って落とさない」「黙って文字を変えない」ため。
- **値の書き**: 有効な UTF-8 のバイト列を、`utf8mb4` の列の値と同じ書き方（`quote`）で書く。
- **スキーマ（`--target mysql`）**: その列の `CHARACTER SET latin1 COLLATE ...` を `CHARACTER SET utf8mb4 COLLATE ...` に書き換える（14.5 の対応表）。**値だけ変えて、スキーマを変えないことはしない**（14.2 の 3 の 1 行目。データが静かに失われる）。テーブルの既定の文字セット（`DEFAULT CHARSET=latin1`）は変えない。指定していない列と、新しく足される列のためである。
- **`DEFAULT`、`COMMENT`、`ENUM` / `SET` のメンバー**: 指定された列に非 ASCII があれば、最初の実装では**エラーにする**（「対応していない」）。`information_schema` の `COLUMN_DEFAULT`、`COLUMN_COMMENT`、`COLUMN_TYPE` で非 ASCII を検出できる。付け替える実装は、必要が出てから。
- **`--target sqlite` / `postgres`**: スキーマに文字セットがないので、スキーマは変わらない。値だけ UTF-8 のテキストになる。`DEFAULT` 等の扱いは同じ。
- **指定が何にも一致しない、`latin1` 以外の列に一致した**: エラーにする（指定の間違いを黙って無視しない）。`*` が `latin1` 以外の列に当たったときは、その列を飛ばす（`*` は「`latin1` の列」を意味する）。`latin1` の列が 1 つもなければ、エラーにする。

**【却下】検出して自動で切り替える（`auto`）。** 1 行の新しい行が不正なバイトを持っただけで、その列のスキーマと全行の表現が変わる。差分の局所性に反し、データを足すだけで出力の形が変わる。検出は `check`（C12）の報告に使う（14.4）。

**【却下】スキーマを `latin1` のまま、バイト列を 16 進リテラル（`0xE38182...`）で書く。** 復元は正しい（14.2 の 3）が、今の既定の出力も `latin1` のまま復元すれば正しいので、得るものがない。SQLite と PostgreSQL では使えず、ファイルの可読性も下がる。

**【却下】全体に効くフラグ（`--latin1-as-utf8` だけ）。** 同じ DB に、実体が UTF-8 の列と本物の `latin1` の列が混在することがある（14.2 の id 4 と id 6）。指定は列の単位にする。`*.*.*` を指定すれば全体と同じになる。

### 14.4 検出（`check` の C12）

`check` は、`latin1` の列ごとに、14.2 の 5 の集計問い合わせを流す（`WHERE` は、非 ASCII を含む行だけに絞る。値の検査なので、`--no-scan` で省く）。結果を 4 つに分ける:

| 分類 | 条件 | 報告 |
| --- | --- | --- |
| 非 ASCII なし | `non_ascii = 0` | 何も出さない |
| 全部が有効な UTF-8 | `invalid = 0` かつ `non_ascii > 0` | **warn**: 「実体は UTF-8 の可能性が高い。`utf8mb4` の列や SQLite / PostgreSQL に移すなら `--latin1-as-utf8 DB.TABLE.COLUMN` を使う」。`non_ascii` の件数と、例のキー（最大 5 件）を示す |
| 全部が不正 | `valid_utf8 = 0` かつ `non_ascii > 0` | **info**: 宣言どおりの `latin1`（または `cp932` などの別の文字コード）の可能性が高い。何もしなくてよい |
| 混在 | `valid_utf8 > 0` かつ `invalid > 0` | **warn**: 「UTF-8 と別の文字コードが混ざっている。`--latin1-as-utf8` はエラーになる。データを直すか、列を指定しない」。件数と、不正な行の例のキー（最大 5 件）を示す |

**検出で分からないこと**（報告の文言に入れる）:

- **全部が有効な UTF-8 でも、実体が本物の `latin1` のことがある**。たとえば本物の `latin1` の `Ã©`（`C3 A9`）は、UTF-8 としても有効（`é`）である。自然な文章では偶然に有効になることは少ないが、短い値だけが入った列では見分けられない。件数が多いほど確かで、少ないときは「可能性」とだけ言う。
- **不正なバイトが 1 つでもあれば、その値は UTF-8 ではない**ことは確実である。
- 実体が UTF-8 以外の文字コード（`cp932` / `Shift_JIS`、`EUC-JP` など）の列は、不正と分類される。この案は UTF-8 だけを扱う。
- すでに二重エンコードされたデータ（過去の誤った移行の結果）は、有効な UTF-8 と分類され、直すと「正しい二重エンコードの文字」になる。検出できない。

ダンプ本体は、この集計をしない（全行の追加の読み出しになる）。ダンプのログに「`check` で調べられる」と足す案も、ログが増えるので入れない。案内は README に書く。

### 14.5 `--target mysql` のスキーマの書き換え

指定された列の `COLLATE` の対応（`latin1` → `utf8mb4`）:

| `latin1` の照合順序 | `utf8mb4` の照合順序 | 理由 |
| --- | --- | --- |
| `latin1_swedish_ci`（`latin1` の既定） | `utf8mb4_general_ci` | 5.5.3 以降のすべての MySQL と MariaDB にある。`utf8mb4_0900_ai_ci`（8.0 以降）は MariaDB と 5.7 にない |
| `latin1_general_ci` | `utf8mb4_general_ci` | |
| `latin1_bin` | `utf8mb4_bin` | |
| それ以外（`latin1_general_cs`、`latin1_german1_ci`、`latin1_german2_ci`、`latin1_danish_ci`、`latin1_spanish_ci`） | **エラー** | 対応する照合順序がなく、並びと一致の規則が変わるため |

- 照合順序は、ファイルの並びには関係しない（`ORDER BY` はバイト順）。ただし**復元後の一意キーの一致の規則は変わる**。`latin1_swedish_ci` が文字化けの文字（`ã` など）に対して決めていた一致が、本物の文字に対する `utf8mb4_general_ci` の一致に変わるので、元で別の値だったものが同じ値になり、復元が重複エラーで止まることがある。止まるのであって、静かには壊れない。
- 長さの上限: `VARCHAR(n)` は、`latin1` では n が 65535 バイト以内、`utf8mb4` では 4n が 65535 バイト以内（n は 16383 文字まで）。`latin1` の `VARCHAR(20000)` は復元でエラーになる。インデックスの長さの上限（古い行形式の 767 バイトなど）も 4 倍になる。**どちらもエラーで止まる**（静かには壊れない）。
- SHOW CREATE の書式: 8.4 では、テーブルの既定と同じ文字セットの列にも `CHARACTER SET latin1 COLLATE latin1_swedish_ci` が書かれた。5.7 などでテーブルの既定と同じ列の句が省かれる場合は、句を足す書き換えになる（未確認。14.8）。

### 14.6 決定性と差分の局所性

- 指定された列のスキーマの書き換えは、列の定義と指定だけで決まる（データに依存しない）。
- 値の書き方は、その値のバイト列だけで決まる（行の位置や件数に依らない）。1 行のデータの変更は、その行の変更だけになる。
- 不正なバイトの行は、スキーマを変えずに**エラー**になる。データの追加で、出力の形が変わらない。
- `INSERT` の区切りは行の内容のハッシュで決まる（4.2 節）。指定した列の値の文字が変わると、その行の区切りの判定も変わる。これは、フラグの指定を変えたときだけの一度きりの差分である。
- キーのない表のメモリ上の整列（`EachRow`）は、MySQL のリテラルの文字列の順なので、フラグを付けると並びが変わりうる。付けても同じ入力には同じ出力になる（決定性は保たれる）。

### 14.7 実装の分け方（案）

実装は、この案を見てから別項目にする（この項目では積まない）。分け方の案:

1. 指定の解析と、指定の検証（`DB.TABLE.COLUMN`、`*`、一致なしのエラー、`latin1` 以外のエラー）。
2. 読みと値の書き（`CAST(col AS BINARY)`、`utf8.Valid`、エラーの文言）。`--target sqlite` と `postgres` を含む。統合テストは、14.2 の表（id 1 から 8）を入れた DB を使う。
3. `--target mysql` のスキーマの書き換え（14.5 の対応表、`DEFAULT` / `COMMENT` / `ENUM` の非 ASCII のエラー）。
4. `check` の C12（14.4）。13.9 の 5 の順（`check` の骨組みの後）。
5. README: 「The output」の節に、このフラグと、14.2 の 3 の表（サーバー側の直し方。`CONVERT TO` は二重エンコードになる）を書く。

### 14.8 確認していないこと

- 5.7、5.6、MariaDB での挙動（読み方の表、復元の表）。8.4 と同じと推測しているだけで、「Tested」にしてはならない。
- 8.0。
- `SHOW CREATE TABLE` が、テーブルの既定と同じ文字セットの列の `CHARACTER SET` を省く版（14.5）。
- `utf8mb3` の列に入った別の文字コード、`latin1` 以外の文字セット（`latin2`、`sjis`、`cp932` など）の列に入った UTF-8。
- `--latin1-as-utf8` の実装（コードは変えていない）。上の「【決定】」は案の決定であり、実装で変えうる。
- 識別子（表と列の名前）に UTF-8 のバイトが入っている場合。

## 15. ダンプの差分の要約の設計案（B-083）

この節は**案**であり、コードは変えていない。ダンプの終わりに「3 ファイルが変更、`orders` に +120 / −5 行、`users` は変更なし」のような、**前回の出力との差の要約**を出す機能の形を決める。git には依存しない（出力ディレクトリが git 管理でなくても動く）。

確定した方針（B-083 の仕様確定でユーザーが選んだ）: 既定では出さず `--summary` を付けたときだけ出す。人が読む形は標準出力、ログは今までどおり標準エラー出力。機械が読める形は `--summary-format json`。

### 15.1 位置づけと、前提にする不変条件

- 出力は 1 レコード 1 行で、`INSERT` の区切りは行の内容で決まる（4.2、`--rows-per-file` も同じ）。そのため、前回の出力と今回の出力は**行の単位で比べられる**。これがこの機能の前提であり、新しい出力形式は要らない。
- 要約は**出力ファイルの内容を変えない**。要約を出すかどうかで、全ファイルがバイト単位で同じになる（決定性）。要約は標準出力にだけ出す。
- 要約は**行の値を含まない**。含むのは、DB 名、表名、ファイル名、件数だけ（個人情報がログや標準出力に出ない。README の Security の節と矛盾しない）。
- 比べる相手は、**今回の実行が書き換える前の、出力ディレクトリの同じ名前のファイル**である。前回のダンプが git の履歴にあるかどうかは見ない。

### 15.2 要約の内容

**単位**: 要約は次の「単位」ごとに 1 件を出す。ファイルではなく論理的な単位にするのは、`--rows-per-file` でファイルが増減したとき、部品のファイル名が変わるだけで行は同じ、という場合に行の増減が出ないようにするためである（4.1 の【実装】のとおり、テーブルが 1 ファイルから複数ファイルに増えると名前が変わる）。

| 単位 | 含まれるファイル | 数えるもの |
| --- | --- | --- |
| `schema` | `<db>.00-schema.sql` | 行（`+n / −m 行`） |
| `data` | `<db>.10-data.<table>.sql`、または `<db>.10-data.<table>.0001.sql`, `.0002.sql`, ... の全部（部品の番号順につないで 1 つの流れとして比べる） | 行（レコード。`+n / −m 行`） |
| `triggers` | `<db>.20-triggers.sql` | 行 |
| `constraints` | `<db>.90-constraints.sql` | 行 |

部品のファイル名からテーブル名を取り出すには、`naming` に、`DataFileParts` の逆を行う関数を足す。エスケープ後のテーブル名には `.` が入らない（`naming` の規則）ので、`<db>.10-data.` の後ろの最初の `.` までがテーブル名、その後ろの 4 桁以上の数字が部品の番号、と曖昧さなく分けられる。

**状態**（単位ごと）:

| 状態 | 意味 |
| --- | --- |
| `added` | 前回のディレクトリになく、今回書く（新しい DB、新しいテーブル） |
| `removed` | 前回あり、今回は書かない（テーブルを落とした。実行が消す古いファイル。`removeStale` と同じ集合） |
| `changed` | 両方にあり、内容（バイト列）が違う |
| `unchanged` | 両方にあり、全ファイルがバイト単位で同じ |

- **`removed` の集合は `removeStale` が消す集合と一致させる**（同じ `ownedEntries` で数える）。出力ディレクトリにある、このツールが管理しない名前のファイルは、数えない（今のダンプも触らない）。要約に出たファイルと、実際に書き換わったり消えたりしたファイルが一致することを、テストの条件にする（15.7 の実装の分け方に書く）。
- **行の数え方**: 更新された行は、**1 行の削除と 1 行の追加**になる（`+1 / −1`）。要約が見るのはテキストで、前回のファイルの行から主キーを取り出すこと（SQL のリテラルを解析すること）はしない。主キーで対応づけて「更新」を数える案は、行の解析が要り、値の表現（4.2、5 節）に依存するので採らない。
- **行の同一性は、行末の `,` と `;` を除いて決める**。`INSERT` の区切りは行の内容で決まる（4.2）が、行が増減すると、その近くの行の末尾が `,` から `;` に、またはその逆に変わる（行が文の最後かどうか）。末尾を含めて比べると、中身が同じ行が「1 行消えて 1 行増えた」と数えられる。次の模擬の実験で確かめた（確認日: 2026-11-12。Python の模擬で、5000 行のテーブルに 1 行の削除、1 行の挿入、1 行の更新をした。区切りは行のハッシュで決めた）: 末尾を含めて比べると +2 / −3、末尾を除くと本来の +2 / −2 になった。この模擬は本ツールの `datafile.Stream` そのものではなく、実装の項目では本物の出力で確かめる。
- **データでない行**（ファイルの固定ヘッダー、`INSERT INTO ... VALUES` の行、ファイルの終わりの行）は、行の数に入れない。固定なので変わらないが、`--rows-per-file` や区切りの変化でこれらの行は増減する。行の数は、**行の本体が `(` で始まる行**だけを数える。
- **`schema` / `triggers` / `constraints`** は、行の区切りの規則がない（`;` が本体に入る。トリガーは 4.2 の対象外）ので、**行をそのまま**（末尾を除かずに）比べて、`+n / −m 行` を出す。
- **変更なし**: 人が読む表では、変更のない単位は**一覧に出さず、合計の行に件数だけ**入れる（巨大なディレクトリで、変更のない数千のテーブルが表を埋めないため）。JSON では、**変更のない単位も 1 行ずつ出す**（機械が、全部を決まった形で受け取れる。絞り込みは受け取り側）。
- **出力ディレクトリが空、または存在しない**（初回）: 全部が `added`。`added` の行数は、今回書いた行を数える。`removed` の行数は、前回のファイルを読んで数える。
- **複数 DB**: `--jobs` や DB の指定順によらず、結果は DB 名、単位の種類（`schema`、`data` の表名順、`triggers`、`constraints`）の順に並べる。この順は、出力ファイル名の辞書順と同じである。

### 15.3 出力の形

**人が読む表**（`--summary`。既定の形式）。標準出力に出す。例:

```
summary: 3 changed, 1 added, 0 removed, 41 unchanged (files: 5 changed, 2 added, 0 removed)
  changed  shop.schema              +2 -1 lines
  changed  shop.data.orders         +120 -5 rows  (2 -> 3 files)
  added    shop.data.coupons        +300 rows
```

- 先頭の 1 行が合計（単位の数と、ファイルの数）。単位ごとの行は、変更のあるものだけ。
- 変更のない実行では、先頭の 1 行だけ（`summary: 0 changed, 0 added, 0 removed, 44 unchanged (files: ...)`）。何も出ないのではなく、**要約を頼んだら必ず合計の行が出る**（出なかったことと、変更がなかったことを区別するため）。
- `(2 -> 3 files)` は、`--rows-per-file` で部品の数が変わったときだけ付ける。

**機械が読める形**（`--summary-format json`）。JSON Lines。単位ごとに 1 行、最後に合計の 1 行:

```
{"kind":"unit","db":"shop","unit":"data","table":"orders","status":"changed","added":120,"removed":5,"unit_of":"rows","files_before":2,"files_after":3,"exact":true}
{"kind":"total","units":{"changed":3,"added":1,"removed":0,"unchanged":41},"files":{"changed":5,"added":2,"removed":0,"unchanged":..},"added":420,"removed":6,"exact":true}
```

- `table` は `data` のときだけ。`unit_of` は `rows`（`data`）か `lines`（それ以外）。
- 時刻や所要時間を入れない（15.1。決定性）。

**`--summary-format`**: `table`（既定）か `json`。`--summary` なしで `--summary-format` だけを渡したら、使い方のエラーにする（黙って無視しない）。値が `table` と `json` 以外でも使い方のエラー。

**標準出力と終了コード**: 要約は標準出力にだけ出す（ログは標準エラー出力。README の「エラーと警告は標準エラー出力」と、13.5 の `check` の流儀と同じ）。`--summary` がなければ、標準出力には今までどおり何も出ない。終了コードは変えない（0 と 1。差があっても 0）。`git diff --exit-code` のような「差があれば 1」は、この案に入れない（`--summary-format json` を読めば足りる。要るなら別の項目にする）。

### 15.4 比べる相手の読み方と、大きなテーブルで時間とメモリを増やさない方法

**いつ比べるか**: 全 DB を一時ファイルに書き終えた後、**commit の前**に比べる。commit の後は前回のファイルが残らない（置き換わる）ので、比べるなら commit より前しかない。前回のファイルは commit まで手つかずで残る（README の「Failure handling」）ので、新しい一時ファイルと、前回の最終のパスのファイルを、**読み取りだけで**比べられる。

**読み方**: 2 つのファイルを、1 行ずつ**ストリームで**読む（一度に全体を読み込まない）。行の長さは、`BLOB` の 16 進で数 MB になりうるので、行を保持せず、**行ごとにハッシュ（FNV-1a の 64 ビット）と長さだけ**を取って比べる。`data` の単位は、前回の部品を番号順に、今回の部品を番号順に、それぞれ 1 本の流れにつないで読む。

**比べ方（順序が同じことを使う）**: 前回と今回の行は、同じ順序（4.2 と 5 の並び順）で並んでいる。だから、2 本の流れを**先頭から同時に進め**、一致する間は進め、食い違ったら、**前方の限られた窓の中**で、次に一致する行を探す（行の追加は今回の側だけ、削除は前回の側だけが進む）。窓の中のハッシュだけを持つので、**メモリは窓の大きさで決まり、テーブルの大きさによらない**。

- **窓の大きさ**: 既定は 65,536 行（行あたり 16 バイトのハッシュと長さで 1 MiB 程度）。変更が散らばっていても、食い違いは小さな範囲にとどまるので、窓は足りる。
- **窓を超える食い違い**（連続して数十万行を入れ替えた、並び順の規則が変わった、など）: 窓を、**上限（既定 100 万行。16 MiB 程度）まで倍にして**探し直す。上限でも見つからなければ、そこで探すのをやめ、そこまでを「全部が削除と追加」として数えて、**`exact: false`** を付ける（人が読む表では、数の前に `>=` を付ける）。**黙って近似にしない**。厳密な数が要る場合の方式（ハッシュで分けた一時ファイルを使う外部の突き合わせ）は、この案の範囲外とし、必要になってから足す（その一時ファイルは `.mysqldance-NNNN.tmp` の名前にして、`sweepTemps` が掃除できるようにする）。
- **ハッシュの衝突**: 64 ビットなので、位置まで同じ別の行が一致と判定される確率は無視できる。ただし要約は参考の情報で、復元の正しさには関わらない（出力ファイルそのものは変えない）。
- **速い道**: 前回と今回で、サイズが同じで、バイト列が同じ（先頭から比べて最後まで一致）なら、`unchanged` として行の突き合わせを省く。変更のないテーブルは、前回のファイルを 1 回読むだけで済む。

**時間**: 比べるのは、前回のファイルを 1 回、今回の一時ファイルを 1 回、順に読む費用である。ダンプ自体はテーブルを `SELECT` して書くので、その費用に比べると小さい（読み出しの待ちがなく、ページキャッシュに今回のファイルが残っていることが多い）。ただし**`--summary` を付けない実行に費用を乗せない**（15.5 の既定が `--summary` を求める理由の 1 つ）。実測は、実装の項目で、大きなテーブル（数千万行）で行う（この案では確かめていない。15.8）。

**並列度**: テーブルごとの比べ方は独立なので、`--jobs` のワーカーで並べられるが、**最初の実装は直列にする**（比べる費用は読み出しより小さい。効くと分かってから並べる）。

**複数ファイル**: 前回のディレクトリの読み取りは、書き込み側と同じ出力ディレクトリを対象にする。出力ディレクトリが読めない（権限）、前回のファイルが読めない場合は 15.6 のとおり。

### 15.5 フラグの名前と既定

| フラグ | 既定 | 意味 |
| --- | --- | --- |
| `--summary` | 出さない | 前回の出力との差の要約を、commit の後に標準出力へ出す |
| `--summary-format table\|json` | `table` | 要約の形式（`--summary` と一緒に使う） |

- **既定は出さない**（確定した方針）。理由: (1) 比べるには前回のファイルを全部読むので、全ダンプの費用が増える。(2) 標準出力は今は何も出さない。既存のスクリプトが標準出力を前提にしていないとは限らず、既定で出すと**既存の利用者の出力が変わる**。(3) 出力の形式を変える変更は README を更新する不変条件があるが、既定が変わらなければ出力ファイルの形式は変わらない。
- **B-091（完了時の要約を `Info` で出す）とは別のもの**。B-091 は今回の実行の件数（DB、テーブル、行、ファイル数、所要時間）をログに 1 行出す。B-083 は前回との差を標準出力に出す。件数の集計は共有できるが、依存はしない（B-091 は B-053 の後）。
- **B-084（git との連携）とは依存しない**。要約は、将来の `--commit` のコミットメッセージの材料になりうるが、この案では扱わない（B-084 の項目に書いてある）。
- `--summary` は `check`（13 節）には付けない。`check` はファイルを書かない。

### 15.6 失敗時のファイルの扱い（README の「Failure handling」との整合）

結論: **矛盾しない**。次の 4 つを守る。

1. **比較は commit の前に、読み取りだけで行う**。前回のファイルにも今回の一時ファイルにも書き込まない。前回のダンプが残ることは、比較の有無で変わらない。
2. **比較に失敗したら、commit の前の失敗として扱う**。前回のファイルが読めない、読んでいる途中で I/O エラーが出た、Ctrl-C が来た、などは、「commit の前の失敗」（README の 2 つ目）と同じ扱いにする: 最終のパスは何も変わらず、一時ファイルは消え、終了コードは 1。`--summary` は明示して頼まれたものなので、**比較できなかったのに黙ってダンプだけ成功させない**（要約が出なかったことに気づけない）。`--summary` なしの実行は、比較をしないので、この失敗が起きない。
3. **要約は commit が成功してから出す**。比較は commit の前に済ませて結果を持っておき、標準出力に出すのは、commit と古いファイルの削除が終わった後にする。commit が途中で失敗した（新旧が混ざりうる）場合は、要約を出さず、今までどおりエラーにする。「要約が出た」ことが「出力ディレクトリが新しい状態になった」ことの印になる。
4. **比較は新しい一時ファイルを作らない**（15.4 の厳密な数のための外部の突き合わせを足すときは、`sweepTemps` の対象の名前にする）。ディスクの余裕の要件（README の「古いファイルと新しいファイルの両方の分」）は変わらない。

標準出力が閉じていて書けない（パイプの相手が死んだ）場合: 出力ディレクトリはすでに新しい状態になっているので、**ダンプの失敗にはしない**。ただし要約が出せなかったことは、標準エラー出力にエラーとして出し、終了コードは 1 にする（黙って落とさない。出力ファイルは変わっていないことをメッセージに書く）。

killed（`SIGKILL`、電源断）: 比較は一時ファイルしか作らず、commit の前なので、今の「Killed processes」の説明がそのまま当てはまる。

### 15.7 実装の分け方（案）と、別件

実装は、この案を見てから別項目にする（この項目では積まない）。分け方の案:

1. **行の比較の部品**（`internal/summary` のような新しいパッケージ。DB に触れない純粋な関数）: 2 本の行の流れを比べて、追加と削除の数、`exact` を返す。行末の `,` / `;` の正規化、窓の拡張、上限を超えたときの `exact: false`、長い行（数 MB）、空の流れ、片方だけの流れを、テストで押さえる。ここに 15.2 の模擬の再現（本物の `datafile.Stream` の出力を使う）を入れる。
2. **単位への分け方**: `naming` に部品のファイル名からテーブル名と番号を取り出す関数を足し、前回のディレクトリのファイルを単位に振り分ける。`removeStale` と同じ `ownedEntries` を使い、「要約に出たファイル = commit が変えるファイル」をテストする。
3. **`dump.Run` への組み込み**: 書き終えた後、commit の前に比べる。結果は `Options` のコールバックか、`Run` の戻り値の構造体で返す（`Run` の呼び出し側の互換のため、コールバックが穏当。実装の項目で決める）。
4. **CLI と README**: `--summary` / `--summary-format`、表と JSON の出力、README の「Failure handling」と使い方の節。標準出力に何かを出す最初の機能なので、`--version` 以外で標準出力が空でなくなることを README に書く。

別件: この調査で、コードの変更が要る別件は見つかっていない。

### 15.8 確認していないこと

- 巨大なテーブル（数千万行）での比べる時間とメモリの実測。窓の既定（65,536 行）と上限（100 万行）は、模擬と見積もりであって、実測ではない。
- `--target sqlite` / `--target postgres` の出力での、行の同一性（末尾の除き方）。両方とも `datafile.Stream` で `INSERT` の区切りを決めるが、行の形（`(...)` で始まるか）は、実装の項目で本物の出力で確かめる。
- 実行の途中で出力ディレクトリのファイルが他のプロセスに書き換えられた場合の要約（README は同じディレクトリへの同時実行を禁じている）。
- 位置が動かない更新（主キーの値が変わる更新）を「更新」として数える案（15.2 で採らなかった）。
- ファイル名の衝突や長さの制限（B-096 から B-103）で名前を短縮した後の、部品のファイル名の解析。名前の規則が変わったら、15.2 の「部品のファイル名からテーブル名を取り出す」を見直す。
