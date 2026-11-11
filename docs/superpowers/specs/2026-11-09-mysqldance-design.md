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
- 【確認済み】SQLite 向け出力を `sqlite3` コマンドで復元できること（B-069。確認日: 2026-11-12。`mysql:8.0` / `mysql:8.4` から出力）。
  - 方法: 統合テスト `TestSQLiteRestoreWithCommand` と `TestSQLiteRestoreWithCommandSplitTable`（`internal/dump/sqlite_cli_test.go`）。README と同じ `LC_ALL=C cat *.sql | sqlite3 file` で復元し、`modernc.org/sqlite` で開いて、`modernc` での復元と同じ表・問い合わせで MySQL と値を比べた。`sqlite3` の標準エラー出力が空でなければ失敗にする（`sqlite3` は失敗した文のあとも続けるため）。`sqlite3` が無い環境は skip し、CI（`integration.yml`）はログに skip の文言が残れば失敗にする。
  - 結果: 手元の `sqlite3` 3.46.1 で、両バージョンとも行数と値が一致した。CI（`ubuntu-latest`。Ubuntu 26.04）の `sqlite3` も 3.46.1 で、同じ 2 つのテストが通った（PR の CI のログで確認）。さらに Docker の `ubuntu:14.04`（`sqlite3` 3.8.2）に、`sample_main` の出力を流して、エラーなしで全表が復元できた。`ubuntu:12.04`（3.7.9）は、データファイルの `INSERT` が `near ",": syntax error` で失敗した。
  - 出力が使う構文のうち新しい版で入ったもの: 複数行の `INSERT ... VALUES (...),(...)`（3.7.11）、`char()`（3.7.16。改行を含む文字列。SQLite のリリースノートによる。3.7.16 そのものでは未確認）。それ以外（`PRAGMA foreign_keys`、`BEGIN` / `COMMIT`、`X'..'`、`CAST`、`DESC` つきのインデックス、`ON DELETE` / `ON UPDATE`、`DEFAULT CURRENT_TIMESTAMP`）はこれより古い版から使える。**最低の版は 3.7.16**（実機で通ったのは 3.8.2 から）。
  - 確認していないこと: 3.7.16〜3.8.1 の実機。古い版では 1 文の長さの上限（`SQLITE_MAX_SQL_LENGTH`）の既定が 1,000,000 バイトで、`INSERT` の上限の既定（`MaxInsertBytes` = 1048576）より小さい版がありうる。3.8.2 での動作は確かめていない（3.46.1 は 1,000,000,000）。
- 対象バージョンは MySQL 8.0 系以降で決定済み（10.5 節）。MariaDB は B-037 で差異を確認済み（上）、B-038 で対応した（`dialect` に、セッション設定・ビュー依存の取得・既定値の読み方を持たせた。ビュー依存は `VIEW_DEFINITION` の `` `schema`.`name` `` を大文字小文字を区別せずに探す）。5.7 系は B-039 で差異を確認した（上）。対応は B-040。

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
