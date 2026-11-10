# Backlog

## B-015 [todo|P3] 接続と読み取りのタイムアウトを設定できるようにする
- 概要: `mysql.Config` に `Timeout` / `ReadTimeout` / `WriteTimeout` を設定していないため、サーバーが応答しなくなるとダンプが待ち続ける。
- 受け入れ条件:
  - [ ] 接続タイムアウトを指定するフラグを追加し、既定値を決める
  - [ ] 読み取りのタイムアウトは、巨大テーブルの正常な読み出しを打ち切らない既定にする

## B-017 [todo|P3] 行の整形とハッシュの割り当てを減らす
- 概要: 行ごとに `strings.Join` を 2 回する（キーなしテーブルのソート用に `source.Rows` で 1 回、`datafile.Write` で 1 回）。`datafile.Write` は行ごとに `fnv.New32a()` と `[]byte(text)` を割り当て、`text + sep` も連結して新しい文字列を作る。B-044 のストリーム化で `Rows` の形が変わるなら、そちらに合わせて一緒に直す。
- 受け入れ条件:
  - [ ] ベンチマーク（10 万行程度）を追加し、改善前後の割り当て数と時間を記録する
  - [ ] 出力はバイト単位で変わらない（既存のテストが通る）

## B-018 [todo|P3] 文字列キーの ORDER BY がインデックスを使えない影響を測る
- 概要: 文字列キー（`char` / `varchar` / `text` 系）は `ORDER BY CAST(col AS BINARY)` で並べる。列に式を掛けるので、インデックス順では返せず filesort になる可能性がある（`EXPLAIN` で未確認）。想定規模では問題ないが、B-044 の前に影響を把握しておく。
- 受け入れ条件:
  - [ ] 10 万行・100 万行の文字列主キーのテーブルで `EXPLAIN` と所要時間を記録する
  - [ ] 影響が大きい場合の代替案（バイナリ照合順序の指定など）を検討して結果を書く

## B-019 [todo|P3] テーブルごとのクエリ数を減らす
- 概要: `Tables` はテーブルごとに `SHOW CREATE TABLE` と `SELECT ... LIMIT 0`（型名の取得）を実行し、`Views` は `listTables` をもう一度実行する。テーブルが数十を超えると往復の回数が目立つ。
- 受け入れ条件:
  - [ ] `listTables` の結果を使い回し、`Tables` / `Views` で 1 回にする
  - [ ] 型名の取得を `information_schema` の結果で置き換えられるか確かめる（置き換えられない型があれば理由を書く）

## B-023 [todo|P3] 別の DB を参照するビューの復元順を検証する
- 概要: `Views` の依存取得は `TABLE_SCHEMA = 対象 DB` に限られ、別の DB のテーブルやビューを参照するビューは依存として扱わない。複数 DB を同じディレクトリに入れると、復元はファイル名の辞書順なので、参照先の DB が後に来ると `CREATE VIEW` が失敗する可能性がある（未確認）。B-003 とは別に、この制約を確認して README に書くか、警告を出す。
- 受け入れ条件:
  - [ ] 2 つの DB にまたがるビューを実機で復元して、失敗するかを確認する
  - [ ] 失敗する場合は、ダンプ時に警告を出し、README の「Limitations」に書く

## B-024 [todo|P3] パスワードファイルのパーミッションを検査する
- 概要: `--password-file` はパーミッションを見ずに読む。他のユーザーが読めるファイルなら警告を出す。環境変数 `MYSQL_PWD` は、同じホストの他のプロセスから見える場合があることを README に書く。
- 受け入れ条件:
  - [ ] グループまたは他ユーザーが読めるパスワードファイルを指定したときに警告を出す
  - [ ] README の使い方に `MYSQL_PWD` の注意を追記する

## B-028 [todo|P3] コンテナイメージを GitHub Container Registry に公開する
- 依存: B-027
- 概要: `mcprt` は `Dockerfile` と goreleaser の `dockers_v2`（linux/amd64、linux/arm64、`ghcr.io`）で公開している。mysqldance も同じ形にするか、そもそもイメージが必要かを決める。必要なら、ブランチと `main` では `branch.yml` / `main.yml` のように `goreleaser build --snapshot` の成果物からイメージを作って push し、タグではリリースと同時に push する。
- 受け入れ条件:
  - [ ] イメージが必要かどうかを決めて、この項目に結論を書く（不要なら削除する）
  - [ ] 必要な場合、マルチアーキテクチャのイメージが `ghcr.io` に出る

## B-029 [todo|P3] カバレッジを GitHub Pages で公開する
- 依存: B-025
- 概要: `mcprt` の `main.yml` のように、`gotest` の `html-output-dir` に出した HTML を `wtnb75/actions/merge-pages@v1` でまとめ、`actions/deploy-pages` で `main` へのプッシュのたびに公開する。Pages の有効化はリポジトリの設定で行う。
- 受け入れ条件:
  - [ ] `main` へのプッシュでカバレッジの HTML が Pages に公開される
  - [ ] README のバッジからカバレッジのページに行ける

## B-030 [todo|P3] 手動実行の自動修正・依存更新 workflow を作る
- 依存: B-025
- 概要: `mcprt` の `fix.yml`（`wtnb75/actions/gofix@v1`）と `update-deps.yml`（`wtnb75/actions/goupdate@v1`）に倣い、`workflow_dispatch` で手動実行する。実行するとテストを通したうえで PR を作る（`contents: write` と `pull-requests: write` が必要）。
- 受け入れ条件:
  - [ ] 手動実行で PR が作られ、PR 上で CI が通る
  - [ ] 統合テストが必要な更新でも、B-026 の CI で検証される

## B-041 [todo|P2] 複数接続で同一スナップショットを揃える方式を実機で検証する
- 概要: 並列取得では複数接続が同じ時点を見る必要がある。設計書 6 節の案（`FLUSH TABLES WITH READ LOCK` で位置を固定してから、各接続でスナップショットを開始する）が、必要な権限やロック時間を含めて実用になるかを実機で確かめる。この項目は方式の検証だけで、並列化はしない。
- 受け入れ条件:
  - [ ] 8.0 と 8.4 の実機で、複数接続が同一時点の内容を読めることを確かめる
  - [ ] 必要な権限、書き込みを止める時間、InnoDB 以外のテーブルへの影響を記録する
  - [ ] 採用する方式を設計書に書く

## B-042 [todo|P2] テーブル単位の並列取得に対応する
- 依存: B-041
- 概要: B-041 で決めた方式で、テーブルを複数の接続で並列に取得する。取得部はテーブル単位で独立に呼び出せる構造にしてあることが前提（設計書 8 節）。
- 受け入れ条件:
  - [ ] 並列度を指定するフラグを追加し、既定値を決める（既定は 1 か、複数かを README に書く）
  - [ ] 並列度を上げても、全テーブルが同一時点の内容になる
  - [ ] 失敗または Ctrl-C のときに、全ての接続とトランザクションが閉じる

## B-043 [todo|P2] 並列取得の出力が決定的であることをテストする
- 依存: B-042
- 概要: 並列取得でも、出力が並列度によらずバイト単位で同一であることをテストで保証する。ファイルの書き出し順や、警告の出力順が並列度で変わらないことも確認する。
- 受け入れ条件:
  - [ ] 並列度 1、2、8 で同じ DB をダンプし、全ファイルがバイト単位で一致するテストを追加する
  - [ ] 警告の出力順が並列度によらず同じである

## B-044 [todo|P2] キーのあるテーブルを行ストリームで書き出す
- 概要: 今は `source.Rows` が全行を `[][]string` に載せてから書く。主キーまたは NOT NULL のユニークキーがあるテーブルは、サーバーが `ORDER BY` で並べるので、読みながら書けるはず。メモリ使用量が行数に比例しないようにする（設計書 12 節）。キーなしのテーブルは別項目（B-045）で、これまでどおり全行をメモリに載せる。
- 受け入れ条件:
  - [ ] キーのあるテーブルで、メモリ使用量が行数に比例して増えない（100 万行のテーブルで確認する）
  - [ ] 出力はバイト単位で変わらない（既存のテストが通る）
  - [ ] 書き込みの途中で読み出しが失敗したときの挙動（部分ファイルの削除）が変わらない

## B-045 [todo|P3] キーのないテーブルを外部ソートで扱う
- 依存: B-044
- 概要: キーのないテーブルは各行の出力文字列のバイト順で並べるため、全行をメモリに載せてソートしている。大きなテーブルでもメモリを使い切らないように、一時ファイルを使った外部ソートにする（設計書 12 節）。
- 受け入れ条件:
  - [ ] キーのない 100 万行のテーブルで、メモリ使用量に上限がある
  - [ ] 出力は従来のメモリ内ソートとバイト単位で同一である
  - [ ] 一時ファイルは失敗時にも削除され、パーミッションは出力ファイルより緩くない

## B-046 [todo|P2] トリガーをダンプする
- 概要: MVP はテーブルとビューだけが対象（設計書 2 節・12 節）。トリガーを `SHOW CREATE TRIGGER` から出力する。出力先のファイル名（例: `<db>.20-triggers.sql`）と、辞書順で「スキーマ → データ → トリガー」の順になる命名を決める。トリガーはデータのロード後に作る（ロード中に動かさないため）。`DEFINER` の扱いはビューに合わせて決める。
- 受け入れ条件:
  - [ ] トリガーを出力し、復元して定義が一致する
  - [ ] ファイル名が辞書順でデータの後ろになり、`naming` の所有判定（`IsOwnedBy`）に含まれる
  - [ ] 出力は決定的で、`DEFINER` の扱いを README に書く

## B-047 [todo|P3] ストアドルーチンをダンプする
- 依存: B-046
- 概要: ストアドプロシージャとストアドファンクションを `SHOW CREATE PROCEDURE` / `SHOW CREATE FUNCTION` から出力する。ファイル名の方針は B-046 に合わせる。`DELIMITER` が必要な本体の書き方を決める。
- 受け入れ条件:
  - [ ] プロシージャとファンクションを出力し、復元して定義が一致する
  - [ ] 本体に `;` を含むルーチンを復元できる
  - [ ] 出力は決定的である

## B-048 [todo|P3] イベントをダンプする
- 依存: B-046
- 概要: イベントを `SHOW CREATE EVENT` から出力する。ファイル名の方針は B-046 に合わせる。復元先でイベントスケジューラが動いていると、復元と同時に実行される点の扱い（無効な状態で作るかなど）を決めて README に書く。
- 受け入れ条件:
  - [ ] イベントを出力し、復元して定義が一致する
  - [ ] 復元時にイベントが勝手に実行されないようにするか、その扱いを README に書く
  - [ ] 出力は決定的である

## B-049 [todo|P3] `source.go` を役割ごとのファイルに分ける
- 依存: B-020, B-021
- 概要: `source.go`（612 行）は、接続の確立、カタログの問い合わせ、行の読み出しが 1 つのファイルにある。`connect.go`、`catalog.go`、`rows.go` などに分ける。関数の中身は変えない。B-020 と B-021 が同じファイルを触るので、その後に行う。
- 受け入れ条件:
  - [ ] `source` パッケージを役割ごとのファイルに分ける
  - [ ] 関数の中身を変えない（差分は移動だけ）
  - [ ] 既存のテストが通る

## B-050 [todo|P3] `dumpDB` を段階ごとの関数に分ける
- 依存: B-021
- 概要: `dump.dumpDB` は、検査、古いファイルの削除、スキーマの書き出し、データの書き出しを 1 つの関数で行う。段階ごとの関数に分けて、読みやすく、テストしやすくする。B-021 で `Tables` が警告を返すようになった後に行う。
- 受け入れ条件:
  - [ ] `dumpDB` を段階ごとの関数に分ける
  - [ ] 挙動と出力は変わらない（既存のテストが通る）

## B-051 [todo|P3] テスト用のグローバル変数を `Options` に置き換える
- 依存: B-049, B-050
- 概要: `dump.fileWriter` と `source.largeTableRows` はテストが書き換えるためのグローバル変数で、並列実行のテストを妨げる。`Options` のフィールドとして渡す形にする。B-049 と B-050 で触るファイルが変わった後に行う。
- 受け入れ条件:
  - [ ] 2 つのグローバル変数がなくなり、`Options` のフィールドで渡す
  - [ ] 該当するテストが `t.Parallel()` で動く

## B-052 [todo|P2] ロガーを標準の `log/slog` にする
- 概要: 今のログは `dump.Options.Log`（`io.Writer`）に `fmt.Fprintf` で書く警告だけで、レベルがない。`Options.Log` を `*slog.Logger` にし（nil なら出力を捨てる）、既存の警告（非 InnoDB、大きなテーブル）を `Warn` にして DB 名・テーブル名を属性で渡す。ログは標準エラー出力に出す（README の「エラーと警告は標準エラー出力」は維持する）。進捗ログは B-053 で足す。
- 受け入れ条件:
  - [ ] `--log-level`（debug / info / warn / error、既定は info）と `--log-format`（text / json、既定は text）を追加する
  - [ ] 既存の警告が `Warn` で出て、標準出力には何も出ない
  - [ ] 既存の警告のテストを新しい形式に合わせて更新する
  - [ ] README の使い方にフラグを追記する

## B-053 [todo|P3] 進捗を `slog` で出力する
- 依存: B-052
- 概要: DB の開始と終了、テーブルごとの行数・バイト数・所要時間を `Info` で出す。パスワード、接続文字列、行の値はログに出さない。
- 受け入れ条件:
  - [ ] DB とテーブルの進捗が `Info` で出る
  - [ ] ログにパスワード・接続文字列・行の値が出ないことをテストで確認する

## B-054 [todo|P2] 動作確認用のサンプルデータを作る
- 概要: 手元で `mysqldance` を試したり、出力の差分を見たりするための、小さくて多様なサンプルデータを SQL ファイルで用意する。設計書 9 節のテスト方針にある形を網羅する: 改行・引用符・バックスラッシュ・NUL・日本語・絵文字を含む文字列、BLOB / VARBINARY / BIT、外部キー（循環参照を含む）、2 段のビュー、主キーなしのテーブル、生成列と `INVISIBLE` 列、非 ASCII の DB 名、複数 DB。追加のツールを増やさないよう、SQL ファイル（`scripts/sample/`）と `Taskfile.yml` のタスクだけで作る。値は固定にし（`NOW()` や `RAND()` を使わない）、何度作っても同じデータになる。
- 受け入れ条件:
  - [ ] `task sample:up` で Docker の MySQL（バージョンは `VERSION=8.0` / `8.4` で選べる）を起動してサンプルを投入し、`task sample:down` で片付ける
  - [ ] 上に挙げた形がすべて入っている
  - [ ] 同じ手順で 2 回作って `mysqldance` でダンプし、全ファイルがバイト単位で一致する
  - [ ] README の「Development」に使い方を書く

## B-055 [todo|P2] 性能測定用の大量データを作る
- 依存: B-054
- 概要: 行数を指定して大きなテーブルを作る。B-017（割り当て削減）、B-018（`ORDER BY` の影響）、B-044・B-045（ストリーム化）の測定に使う。サーバー側で生成する（再帰 CTE など）ので、データを送る手間がなく速い。キーの種類（整数の主キー、文字列の主キー、キーなし）と、行の大きさ（短い行、BLOB を含む大きい行）を選べる。値は固定で、同じ指定なら同じデータになる。
- 受け入れ条件:
  - [ ] `task sample:big ROWS=1000000` のように行数を指定して作れる
  - [ ] 整数の主キー、文字列の主キー、キーなしの 3 種類のテーブルが作られる
  - [ ] 100 万行を作る所要時間の目安を README に書く

## B-056 [todo|P2] 複数 DB の指定を、出力に触る前にすべて検証する
- 概要: `--db a --db typo` や `--db a --db mysql` では、2 つ目の不備が 1 つ目の DB のファイルを書き換えた後に見つかり、出力先が中途半端になる。B-003 で全 DB が 1 つの接続になったので、`Use` で全 DB の存在とシステム DB でないことを先に確かめられる（B-003 のレビューで指摘。README の「Failure handling」の「Multiple databases」の挙動が変わる）。
- 受け入れ条件:
  - [ ] 存在しない DB やシステム DB が 1 つでも含まれるとき、どの DB のファイルも削除・作成されない
  - [ ] README の「Failure handling」を新しい挙動に合わせて更新する
  - [ ] 同じ指定で 2 回作ったデータが一致する（行のチェックサムで確認する）

## B-057 [todo|P2] FLUSH TABLES WITH READ LOCK の待ち時間に上限を設ける
- 概要: B-005 で非 InnoDB のテーブルがあるとき `FLUSH TABLES WITH READ LOCK` を取るようにしたが、実行中の長い文を待つ間、後続の文もすべて待たされ、待ちの上限がない（B-005 のレビューで指摘）。上限を設け、超えたらエラーで終了する。
- 受け入れ条件:
  - [ ] ロック取得の待ち時間を指定するフラグ（または `lock_wait_timeout` の設定）を追加し、超えたらエラーにする
  - [ ] 長いクエリが動いているサーバーで、上限内に失敗して他の文を巻き込み続けないことを統合テストで確かめる
  - [ ] README に書く

## B-058 [todo|P3] INSERT の区切りのハッシュを混ぜて、偶数の RowsPerInsert でも偏らないようにする
- 概要: `datafile.Write` の区切りは FNV-1a 32 ビットの生の値を `RowsPerInsert` で割った余りで決める。FNV-1a の下位ビットは入力の下位ビットにしか依存しないため、行が同じ末尾（`')` など）を持つと、偶数の値（50、100 など）で余りが 0 になる行が極端に少なくなりうる（B-009 で `--rows-per-file` の実装中に、末尾が共通のキーで 0 件になることを確認した。B-009 の `Split` はハッシュを混ぜて解決した）。その場合は `MaxInsertBytes` でしか区切られない。ただし、出力を変えると既存のダンプの差分が一度だけ大きく出る。
- 受け入れ条件:
  - [ ] `RowsPerInsert` が偶数のときも、区切りの間隔が平均 `RowsPerInsert` 行に近いことを、末尾が共通の行でテストする
  - [ ] 出力が変わることを README に書く

## B-059 [todo|P2] FLOAT と -0 を復元後も同じビットに戻るように出力する
- 概要: B-016 の検証で、`FLOAT` 列がダンプ → 復元でビット単位では戻らないこと、`FLOAT` / `DOUBLE` とも `-0` が `0` になることが分かった（8.0.x / 8.4.x の両方）。サーバーのテキストプロトコルは `FLOAT` を有効数字 6 桁で出すため、7〜9 桁が必要な値で下位ビットが落ちる（例: `3.4028235e38` → `3.40282e+38`、`16777216` → `1.67772e+07`）。`-0` が落ちる原因は未調査。`DOUBLE` のその他の値（サブノーマル数を含む）は一致した。詳細は設計書 11 節。修正の方法は実装時に決める（`CAST(f AS DOUBLE)` は `0.1` を `0.10000000149011612` にするので表記が変わる点に注意。有効数字 9 桁で出す方法もある）。出力の表記が変わると既存のダンプの差分が一度だけ大きく出る。
- 受け入れ条件:
  - [ ] `FLOAT` の値と `-0` が、復元後にビット単位で元と一致する（`internal/dump` の `TestFloatDoubleRoundTrip` の `FLOAT` と `DOUBLE` が skip でなく通る。8.0 と 8.4。テストの既知の不一致の許容は外す）
  - [ ] 6 桁以下で往復できる値と `DOUBLE` の出力は、できるだけ変えない（変わるなら README に書く）
  - [ ] 出力の表記が変わることを README に書く（決定性は保つ）

## B-061 [todo|P3] `go install` でも `--version` が意味のある値を出すようにする
- 概要: `--version` の `version` / `commit` は ldflags でしか入らず、`go install ...@vX.Y.Z` や `go build` で作ったバイナリは `dev (commit unknown)` と出る。ldflags が空のときに限り、`runtime/debug.ReadBuildInfo()` の `Main.Version` と `vcs.revision` に切り替える。B-032 のレビューで指摘された。
- 受け入れ条件:
  - [ ] ldflags を渡さずに `go build` したバイナリが、VCS の情報があればコミットを表示する
  - [ ] ldflags を渡したビルドの表示は変わらない

## B-062 [todo|P3] SQLite 向けの出力でビューを変換する
- 依存: B-036
- 概要: B-036 の SQLite 向け出力はビューを出力しない（警告のみ）。MySQL の `SHOW CREATE VIEW` は `ALGORITHM` / `DEFINER` / `SQL SECURITY` 句、文字コード指定子（`_utf8mb4'x'`）、`db`.`t` 形式の修飾名、SQLite にない関数（`IF()`、`CONCAT()`、`DATE_FORMAT()` など）を含むため、変換の範囲を決める（単純な `SELECT` だけを通して残りは警告する、など）。
- 受け入れ条件:
  - [ ] 変換できるビューが SQLite にリストアでき、できないビューは理由つきの警告になる
  - [ ] 出力は決定的である

## B-063 [todo|P3] SQLite 向けの出力で、外部キーの参照先が UNIQUE / 主キーでないときに警告する
- 依存: B-036
- 概要: InnoDB は、外部キーが参照する列に UNIQUE でない通常のインデックスがあれば足りるが、SQLite は参照先の列が主キーか UNIQUE インデックスであることを求める。B-036 の出力はこの外部キーをそのまま書くので、リストアは通る（`foreign_keys=OFF`）が、利用者が外部キーを有効にすると、子テーブルへの書き込みが `foreign key mismatch` で失敗する。B-036 のレビューで指摘された。
- 受け入れ条件:
  - [ ] 参照先の列が親テーブルの主キーでも UNIQUE インデックスでもない外部キーは、理由つきの警告になる（外部キーを出力するか落とすかは実装時に決める）
  - [ ] 参照先が主キーまたは UNIQUE の外部キーの出力は変わらない

## B-064 [todo|P3] PostgreSQL 向けの出力でビューを変換する
- 依存: B-035
- 概要: B-035 の PostgreSQL 向け出力はビューを出力しない（警告のみ）。MySQL の `SHOW CREATE VIEW` は `ALGORITHM` / `DEFINER` / `SQL SECURITY` 句、文字コード指定子、`db`.`t` 形式の修飾名、PostgreSQL にない関数（`IF()`、`CONCAT()` の挙動、`DATE_FORMAT()` など）を含むため、変換の範囲を決める（単純な `SELECT` だけを通して残りは警告する、など）。B-062（SQLite 向け）と方針を揃える。
- 受け入れ条件:
  - [ ] 変換できるビューが PostgreSQL にリストアでき、できないビューは理由つきの警告になる
  - [ ] 出力は決定的である

## B-065 [todo|P3] PostgreSQL 向けの出力で、別の DB を参照する外部キーを出力する
- 依存: B-035
- 概要: B-035 の PostgreSQL 向け出力は、DB ごとの制約ファイル（`<db>.90-constraints.sql`）を後ろに置くため、別の DB（別のスキーマ）を参照する外部キーを警告して出力しない（`a.90-constraints.sql` は `b.00-schema.sql` より前に動く）。全 DB の後に動く制約ファイルを足すなど、復元順を保ったまま出力する方法を決める。
- 受け入れ条件:
  - [ ] 別の DB を参照する外部キーが PostgreSQL にリストアされる
  - [ ] 1 つの DB だけをダンプしたときの出力は変わらない

## B-066 [todo|P2] 5.7 のゼロ日付の既定値を 8.x へ復元できるようにする
- 依存: B-040
- 概要: 5.7 で `explicit_defaults_for_timestamp=OFF`（5.7 の既定）のとき、`TIMESTAMP NOT NULL` の列は `SHOW CREATE TABLE` に `DEFAULT '0000-00-00 00:00:00'` と出る。`--target mysql` はこれをそのまま出すので、`NO_ZERO_DATE` を含む既定の `sql_mode` の 8.x では、復元時に `Invalid default value` で失敗しうる（B-040 のコードレビューで指摘。実機では未確認）。まず実機で確かめ、失敗するなら、出力する方法（復元ファイルの `sql_mode` 設定など）を決める。
- 受け入れ条件:
  - [ ] 5.7.44 の `TIMESTAMP NOT NULL`（`explicit_defaults_for_timestamp=OFF`）の出力を、8.4 に復元した結果が分かる（失敗するか、しないか）
  - [ ] 失敗する場合、復元できる出力になる。5.7 から 5.7 への復元は変わらない
  - [ ] README の「MySQL 5.7」節を結果に合わせる

# Done

## B-039 [done|P2] MySQL 5.7 の 8.0 との差異を実機で確かめる
- 概要: 5.7 には `VIEW_TABLE_USAGE` と `INVISIBLE` 列がなく、`CREATE TABLE` の出力形式にも差がある（`dialect.go` のコメントにも記載）。古いバージョンのデータを持ってきたい意向がある（設計書 10.5 節）。実機で差異を確かめ、対応の方針を決める。この項目はコードを変えず、調査結果を設計書に書く。
- 結果: MySQL 5.7.44 を 8.4.13 と並べて確認し（`show_create_table_verbosity` だけ 5.7.21 でも）、設計書 11 節に書いた。`VIEW_TABLE_USAGE` がなく、B-038 の `viewDefinitions` がそのまま使える。`EXTRA` に `DEFAULT_GENERATED` が付かないので、`DEFAULT CURRENT_TIMESTAMP` が SQLite 向けでは固定の文字列 `'CURRENT_TIMESTAMP'` になり、PostgreSQL 向けでは落ちる。外部キーの規則が `RESTRICT`（8.x は `NO ACTION`）。`show_create_table_verbosity` は 5.7.22 から。データはバイト単位で一致し、5.7 の出力は 5.7 と 8.4 に復元できた。方針は `dialect` に集約する。B-040 の受け入れ条件を結果に合わせて直した。
- 受け入れ条件:
  - [x] 5.7 の実機で、ビュー依存の取得、生成列、`SHOW CREATE TABLE` の差異を確かめる
  - [x] 結果と対応の方針（ビュー依存解決のフォールバックなど）を設計書に書く
  - [x] 実装の項目（B-040）の受け入れ条件を、結果に合わせて直す

## B-038 [done|P2] MariaDB に対応する
- 依存: B-037
- 概要: B-037 の調査結果（設計書 11 節）に基づき、MariaDB 向けの `dialect` を足して `dialectFor` の「MariaDB は対象外」のエラーを外す。「バージョン差」ではなく「DBMS の方言差」として扱い、バージョン依存の処理を 1 か所にまとめる方針を方言差にも広げる（設計書 10.5 節）。`dialect` は SQL 文字列だけでなく、セッション設定、ビュー依存の取得、既定値の読み方も持つように広げる。
- 確定した仕様: MariaDB の最小バージョンは設けない（MariaDB と名乗るサーバーはバージョンで拒否しない。確認したのは 10.11 と 11.4 だけで、README に書いた）。`SELECT VERSION()` と `dialectFor` を `SET` より先に行い、セッション設定は `dialect` が持つ。`SEQUENCE` / `SYSTEM VERSIONED` はエラーにする（出力しない）。
- 受け入れ条件:
  - [x] `SET SESSION show_create_table_verbosity` を MySQL だけに適用し、MariaDB 10.11 と 11.4 の両方でスナップショットを開始できる（今は `Unknown system variable` で失敗する）
  - [x] ビュー依存を `VIEW_TABLE_USAGE` なしで取得できる（`VIEW_DEFINITION` の解析など。方式は実装者が決める）。依存したビューが先に来ない並びにならない
  - [x] `COLUMN_DEFAULT` を方言ごとに読む。MariaDB の文字列 `"NULL"` は既定値なし、引用符つきの文字列は引用を外す、式（`current_timestamp()`、`uuid()` など）は式として扱う。SQLite / PostgreSQL 向けの出力に反映される
  - [x] `TABLE_TYPE` が `BASE TABLE` / `VIEW` 以外（`SEQUENCE` / `SYSTEM VERSIONED`）のものを黙って落とさない。方針は「エラーにする」（出力はしない）。ダンプの検査の段階で、古いファイルを消す前に失敗し、テーブル名と種別をエラーに書く。README に書く
  - [x] MariaDB の実機（10.11 と 11.4）で、既存の統合テスト全体（MySQL 固有のテキストを前提にするものは、テスト専用の判定で skip）と、MariaDB 専用の往復テスト（ダンプ → MariaDB への復元 → 行数・値の一致）が通る。`task test:integration:mariadb` で実行する（`task test:integration` の既定には含めない）
  - [x] `dialect` 以外のコードに MariaDB 固有の分岐を入れない
  - [x] README の対象バージョンと、MariaDB の出力は MySQL に流せない（照合順序など）注意を更新する

## B-037 [done|P2] MariaDB の MySQL との差異を実機で確かめる
- 概要: 設計書 12 節に挙げた MariaDB の差異（`SHOW CREATE TABLE` の既定値の書式、照合順序、JSON が `LONGTEXT` + `CHECK`、シーケンス、`VIEW_TABLE_USAGE` がないことなど）は記憶ベースで未確認。実機で確かめ、対応の方針を決める。この項目はコードを変えず、調査結果を設計書に書く。
- 結果: MariaDB 10.11.19 と 11.4.13 を、MySQL 8.4.13 と並べて確認し、設計書 11 節に書いた。現在のバイナリは `show_create_table_verbosity` で失敗する。`VIEW_TABLE_USAGE` は両方ない。`COLUMN_DEFAULT` の読み方（`"NULL"` の文字列、引用符つき、式）が違い、`SEQUENCE` / `SYSTEM VERSIONED` のテーブルは黙って落ちる。方針は `dialect` に集約する。B-038 の受け入れ条件を結果に合わせて直した。
- 受け入れ条件:
  - [x] MariaDB の実機（LTS 版 1 つ以上）で、各差異が実際にあるかを確かめる
  - [x] 結果と、`dialect` に集約する方針を設計書に書く
  - [x] 実装の項目（B-038）の受け入れ条件を、結果に合わせて直す

## B-035 [done|P2] PostgreSQL 向けの出力に対応する
- 依存: B-034
- 概要: PostgreSQL 向けのライタを足す。boolean への変換、外部キーを後付けする順序、識別子のクォートなどの方言差を扱う（設計書 12 節）。
- 前提（仕様確定）: フラグは既存の `--target` に値 `postgres` を足す。MySQL の DB 1 つは PostgreSQL の同名スキーマ 1 つに対応させる（`CREATE SCHEMA IF NOT EXISTS` と `SET search_path`）。`TINYINT(1)` は `boolean`（0/1 以外の値があればエラーにする）、他の `TINYINT` は `smallint`。テーブル定義は SQLite と同じく `Schema.Defs` の構造から組み立てる。ビューは SQLite と同様に警告して出力しない（変換は別項目）。PostgreSQL に写せない定義は黙って落とさず警告かエラーにする。外部キーは全データの後に適用する（復元順に依存しないよう、データファイルの後ろに並ぶファイルへ分ける）。検証は Docker の `postgres:17` と、テスト専用の依存 `pgx`（環境変数 `MYSQLDANCE_TEST_PG_DSN` があるときだけ動く）で行い、`task test:integration` に含める。
- 結果: `--target postgres` を足した。`writer.Postgres` が MySQL の DB 1 つを同名のスキーマに写し、`<db>.00-schema.sql`（`CREATE SCHEMA` とテーブル、インデックス）、`<db>.10-data.<table>.sql`（1 ファイル 1 トランザクション）、`<db>.90-constraints.sql`（外部キー）を書く。外部キーは全データの後に適用するため、`naming.ConstraintsFile` と、`writer.ConstraintWriter`（任意のインターフェース）を足し、`dump` が書き出す。各ファイルは単独のセッションで動くよう自分で `search_path` などを設定する。定義は `TableInfo.Def`（`dump` が `NeedsDefs` のライタにだけ渡す）から組み立てる。`datafile.WriteStatements` は識別子のクォートを引数で受ける。PostgreSQL に入らない値（`TINYINT(1)` の 0/1 以外、NUL、0 や欠けた日付、年 0）と 63 バイトを超える名前はエラーにする。実機の確認は Docker の `postgres:17` とテスト専用の `pgx`（`MYSQLDANCE_TEST_PG_DSN`）で行い、`task test:integration` が MySQL ごとに PostgreSQL も起動する。公式イメージの `/docker-entrypoint-initdb.d` での復元も手で確かめた。
- 確認: `TINYINT(1) UNSIGNED` は MySQL 8.0 / 8.4 が `tinyint unsigned` と報告するため `smallint` になる（`boolean` は素の `TINYINT(1)` / `BOOLEAN` のみ）。
- 受け入れ条件:
  - [x] `--target postgres` で PostgreSQL 向けに出力でき、実機にリストアして行数と値が一致する
  - [x] 出力は決定的である（2 回の出力がバイト単位で一致する）
  - [x] `--target` の値 `postgres` を追加し、README に書く（ビューを出力しないことなども書く）

## B-036 [done|P2] SQLite 向けの出力に対応する
- 依存: B-034
- 概要: SQLite 向けのライタを足す。型の変換や、制約の書き方などの方言差を扱う（設計書 12 節）。出力先を選ぶフラグは B-035 で追加するものを使う。B-035 と並行して進めるなら、先に入ったほうのフラグ定義に合わせる。
- 前提（仕様確定）: フラグは `--target mysql|sqlite`（既定 `mysql`、不明な値はエラー）。B-035 は値 `postgres` を足す。テーブル定義は `information_schema` から構造（列の型・NULL 可否・既定値、主キー、UNIQUE、外部キー、通常のインデックス）で読み、SQLite の DDL を組み立てる（`SHOW CREATE TABLE` は解析しない）。ビューは MySQL の `SELECT` 文が SQLite でそのまま通らないため、警告を出して出力しない（変換は B-062）。SQLite に写せない定義（生成列など）は黙って落とさず、警告かエラーにする。実機の検証には純 Go の `modernc.org/sqlite` をテスト専用の依存として足す（`sqlite3` コマンドはこの環境にない）。
- 結果: `--target mysql|sqlite` を足した。`source.Snapshot.Defs` が `information_schema` から列・インデックス・外部キー・CHECK の名前を構造で読み、`writer.SQLite` が SQLite の DDL と `INSERT` を書く。構造が要るライタだけが `Writer.NeedsDefs()` で `true` を返し、MySQL の出力と読み出しは変わらない。値は `sqlvalue.SQLiteLiteral` で書く（`DECIMAL`・`BIGINT UNSIGNED`・`BIT(64)` は `TEXT`、行の途中の改行は `char(10)` の連結、改行が 100 を超える値は 16 進の `CAST`）。警告は `Schema.Warn` 経由で `dump` のログに出る。実機の SQLite は純 Go の `modernc.org/sqlite`（テスト専用）で確認した。
- 受け入れ条件:
  - [x] `--target sqlite` で SQLite 向けに出力でき、実機の SQLite にリストアして行数と値が一致する
  - [x] 出力は決定的である（2 回の出力がバイト単位で一致する）
  - [x] `--target` のフラグを追加し、README に書く（ビューを出力しないことも書く）

## B-034 [done|P2] ライタをインターフェースにして差し替えられるようにする
- 依存: B-033
- 概要: ライタは MySQL 向けの `datafile` / `schemafile` だけで、`dump` がそれを直接呼ぶ。出力ライタをインターフェースにし、MySQL 向けをその実装の 1 つにする（設計書 8 節）。他 DBMS のライタは別項目（B-035、B-036）で足す。
- 結果: 新パッケージ `internal/writer` に `Writer`（`WriteSchema`、`DataParts`）と MySQL 向けの実装 `MySQL` を置いた。`dump` は `Options.Writer`（nil なら `Options.Data` から作る MySQL 向け）経由で書く。両方を指定するとエラー、`DataParts` が 0 件を返してもエラー。MySQL 向けの出力はバイト単位で同一。
- 前提: インターフェースと MySQL 向けの実装は新パッケージ `internal/writer` に置く。値のリテラル化、行の分割、ビューの並べ替え、`AUTO_INCREMENT` / `DEFINER` の除去は MySQL 向けの実装に移し、`dump` には残さない。ファイル名・暗黙のステージング・古いファイルの削除は `dump` に残す。フラグは変えない（DBMS を選ぶフラグは B-035）。
- 受け入れ条件:
  - [x] ライタのインターフェースを定義し、`dump` がそれ経由で書く
  - [x] MySQL 向けの出力はバイト単位で変わらない（既存のテストが通る）

## B-033 [done|P2] `source` が型付きのレコード値を返すようにする
- 概要: MVP の `source.Rows` は値を SQL リテラル文字列にして返す（設計書 8 節の「型付きのレコード値」からの意図的な逸脱）。他 DBMS 出力の前提として、列の型情報つきの型付き値を返す形にする。MySQL 向けのリテラル化は `sqlvalue` に残し、出力のバイト列は変えない。
- 結果: `sqlvalue.Value`（型名・バイト列のコピー・NULL）を足し、`Rows` が `[][]sqlvalue.Value` を返す。`dump` が `sqlvalue.Literals` で MySQL のリテラルにしてから `datafile` に渡す。`datafile` のテストは変更なし。コンパイルのため、テストのヘルパー `rowsOf` だけがリテラルに変換するようにした。
- 受け入れ条件:
  - [x] `source.Rows` が型付き値を返し、`sqlvalue` がそれを MySQL のリテラルにする
  - [x] 既存の単体・統合テストが変更なしで通り、出力はバイト単位で同一である

## B-021 [done|P2] `Snapshot` の隠れた状態をなくす
- 概要: `Warnings()` は直前の `Tables()` の呼び出し結果に依存する。`Rows` は `Tables` が埋める `s.columns` のキャッシュに依存し、無ければ読み直す。呼び出しの順序を間違えると、警告が空になったり余計なクエリが走ったりする。
- 結果: `Tables` が `([]TableInfo, []string, error)` を返す。キーの列の型は `TableInfo.OrderByTypes` に持たせ、`Snapshot` から `warnings` と `columns` を除いた。`Rows` はカタログを読まない。キーの型が欠けていると、照合順序で並べずにエラーにする。
- 受け入れ条件:
  - [x] `Tables` が警告を戻り値として返す（`Warnings()` を廃止する）
  - [x] キーの列の型を `TableInfo` に持たせ、`Rows` から `s.columns` への依存をなくす
  - [x] 既存のテストが通る

## B-020 [done|P2] 重複した `quoteIdent` を 1 か所にまとめる
- 概要: 同じ実装の `quoteIdent` が `source`、`datafile`、`schemafile` に 1 つずつある（3 か所）。識別子のクォートは安全性に関わるので、修正が片方だけに入る事故を防ぐ。
- 結果: `sqlvalue.QuoteIdent` に集約した。`dump` の統合テスト用の `quote` も同じ関数に置き換えた。
- 受け入れ条件:
  - [x] 1 か所に集約し、3 パッケージがそれを使う
  - [x] バッククォートを含む識別子のテストを 1 か所に集める

## B-016 [done|P2] FLOAT / DOUBLE のラウンドトリップ精度を検証する
- 概要: `sqlvalue.Literal` は `FLOAT` / `DOUBLE` をテキストプロトコルの文字列のまま出す。サーバーがこの文字列を何桁で出すかによっては、復元後に値が変わる可能性がある（未確認。設計書 5 節の「浮動小数点の表記」にも検証の記載がある）。この項目は検証だけを行う。不一致が見つかったら、修正は別の項目として積む。
- 結果: `DOUBLE` は `-0` を除いて一致、`FLOAT` は不一致（有効数字 6 桁で出るため）、`-0` は両方で符号が落ちる。修正は B-059。
- 受け入れ条件:
  - [x] 8.0 と 8.4 の実機で、ダンプ → 復元 → 元の値とビット単位で一致するかをテストする（`0.1`、`1e38`、`3.4028235e38`、`1.7976931348623157e308` などを含む）
  - [x] 結果（一致か、不一致ならその値）を設計書の 11 節に書く
  - [x] 不一致があれば、修正を新しい項目として `BACKLOG.md` に追加する

## B-013 [done|P2] 出力ファイルのパーミッションを指定できるようにする
- 概要: 出力ファイルは `0o666`（umask 適用）、ディレクトリは `0o755` で作られ、既定では同じホストの他ユーザーが読める。README に注意は書いてあるが、ツール側に絞る手段がない。ただし絞りすぎると `docker-entrypoint-initdb.d` として使うとき、コンテナ内の `mysql` ユーザーが読めなくなる。
- 確定した仕様:
  - フラグ `--file-mode MODE` と `--dir-mode MODE`（8 進数。`0640`・`750` など）。指定したときは umask に関係なくそのモードにする。
  - 既定は現状維持（ファイルは `0666`、ディレクトリは `0755`、どちらも umask 適用）。`docker-entrypoint-initdb.d` で読めなくなるのを避けるため。README に書く。
  - `--dir-mode` は、この実行が作った出力ディレクトリだけに効く。既存のディレクトリと、一緒に作る親ディレクトリのモードは変えない。
  - 検証: 9 ビットの権限だけを受け付ける。ファイルは所有者が読めること（`0400` 以上）、ディレクトリは所有者が `rwx` を持つことを要求する。
- 受け入れ条件:
  - [x] ファイルとディレクトリのモードを指定するフラグを追加する（既定は現状維持か絞るかを決めて README に書く）
  - [x] 既存の出力ディレクトリのモードは変えない

## B-009 [done|P2] 巨大テーブルを主キー範囲で分割して出力する
- 概要: ファイル名の末尾に番号を足す（`mydb.10-data.orders.0001.sql`）。辞書順にロードして正しく復元できること（設計書 4.1 節・12 節）。
- 確定した仕様:
  - フラグ `--rows-per-file N`（1 ファイルあたりの平均行数）。既定 0 = 分割しない（既存の出力は変わらない）。
  - 境界は内容で決める。主キー（なければ並べ替えに使う一意キー）の列の値の FNV-1a 32 ビットハッシュ（混ぜたもの）を N で割った余りが 0 の行の後でファイルを閉じる。キーが更新されない限り、1 行の更新で境界は動かない。キーのないテーブルは行の文字列のハッシュを使う。
  - ファイル名: 分割した結果が 2 ファイル以上のときだけ `<db>.10-data.<table>.0001.sql` のように番号を付ける。1 ファイルのときは `<db>.10-data.<table>.sql`（従来どおり）。番号は 4 桁以上のゼロ詰めで、ファイル数の桁数が 4 を超えるときは全ファイルを同じ桁数にそろえる（辞書順を保つため）。
  - 各ファイルは固定ヘッダーを持つ（単独でも流せる）。ファイルの終わりでは `INSERT` 文も閉じる。
- 受け入れ条件:
  - [x] 分割後のファイルを辞書順にロードして、元と同じデータに復元できる
  - [x] 1 行を更新したときの差分が該当する 1 行だけである

## B-014 [done|P2] SIGTERM を処理し、終了時のクリーンアップを確実にする
- 概要: `signal.NotifyContext` は `os.Interrupt` だけを受ける。`docker stop` や `kill` が送る SIGTERM では、書き込み中のファイルが途中で切れたまま残り、トランザクションもサーバー側のタイムアウトまで残る。また `Snapshot.Close` の `ROLLBACK` は `context.Background()` で実行するため、接続が切れていると待ち続ける可能性がある。B-004 のアトミック化とは別に、プロセス終了時の振る舞いとして直す。
- 確定した仕様:
  - SIGINT と SIGTERM を同じ経路（`signal.NotifyContext`）で受ける。
  - `Close` の `ROLLBACK` は 5 秒で打ち切る。超えても接続を閉じるので、サーバー側のトランザクションはそこで終わる。
- 受け入れ条件:
  - [x] SIGTERM でも Ctrl-C と同じ経路で中断し、書き込み中のファイルを削除して終了コード 1 で終わる
  - [x] `Close` の `ROLLBACK` にタイムアウトを設ける

## B-005 [done|P2] 非トランザクションテーブル（MyISAM など）の一貫性を確保する
- 概要: MVP は警告して続行する。`FLUSH TABLES WITH READ LOCK`（FTWRL）で取得する（`LOCK TABLES` は InnoDB のスナップショットのトランザクションを暗黙にコミットするため使わない）。対象の DB に非 InnoDB の基本テーブルがあるときだけ、FTWRL → `START TRANSACTION WITH CONSISTENT SNAPSHOT` の順に開始し、ダンプが終わる（`Close`）までロックを保持する。非 InnoDB が無ければ従来どおりロックしない。古いバージョンのデータを持ってくる話と合わせて扱う（設計書 12 節）。
- 確定した仕様:
  - 既定: 非 InnoDB があるとき自動でロックする。`--no-lock` を付けるとロックせず、従来の「警告して続行」に戻る。
  - ロックを取れない（RELOAD 権限が無いなど）ときはエラーで終了する。メッセージに `--no-lock` での回避を書く。
  - ロックを取らずに始めたのに、後で非 InnoDB のテーブルが現れたとき（ダンプ中の `CREATE TABLE`）もエラーにする（黙って非一貫にしない）。
  - 非 InnoDB の警告は、ロックを取ったときは出さない（一貫しているため）。`--no-lock` のときは従来どおり出す。
- 受け入れ条件:
  - [x] MyISAM を含む DB のダンプで、ロックを使って一貫した内容を取得できる（ダンプ中、他の接続の MyISAM への書き込みが待たされる）
  - [x] InnoDB だけの DB ではロックを取らない（他の接続の書き込みが待たされない）
  - [x] `--no-lock` でロックを取らず、非 InnoDB の警告が出る
  - [x] RELOAD 権限が無いときはエラーになり、`--no-lock` を案内する
  - [x] ロックによる影響（書き込みの待たせ方、必要な権限、`--no-lock`）を README に書く

## B-003 [done|P2] 複数 DB を 1 つのスナップショットで読む
- 概要: 現状は DB ごとに別の接続・別のスナップショットで読むため、DB をまたぐ外部キーやビューの整合性は保証されない。複数 DB を 1 接続・1 トランザクションで読む形にする（設計書 12 節）。
- 前提: 接続は 1 本・トランザクションは 1 つ。DB の切り替えは `USE` で行い、DB ごとのキャッシュ（列カタログ、警告）は切り替えで捨てる。1 DB のときの出力と挙動は変えない。フラグの追加はしない。DDL（スキーマ変更）はトランザクションの対象外で、`information_schema` や `SHOW CREATE` は MVCC されない（従来どおり）。
- 受け入れ条件:
  - [x] 複数 DB の指定時、全 DB が同一時点の内容になる（最初の DB の読み出し後に別の DB を更新しても、後続の DB のダンプに現れない）
  - [x] README の「Limitations」から該当項目を削除する

## B-012 [done|P1] TLS で MySQL に接続できるようにする
- 概要: `source.Open` は `mysql.NewConfig()` に TLS を設定しておらず、フラグもない（コードを読んだ範囲。`rg -i 'tls|ssl'` でヒットなし）。リモートの MySQL に TCP で繋ぐと、パスワード認証の交換と全行のデータが平文で流れる可能性がある。個人情報を含みうるダンプなので優先度を上げる。着手時に、現状の接続が平文であることをパケットまたはサーバー側の `Ssl_cipher` で確かめる。
- 受け入れ条件:
  - [x] `--ssl-mode`（`disabled` / `preferred` / `required` / `verify-ca` / `verify-identity`。既定は `preferred`）と `--ssl-ca FILE` を追加する。`preferred` はサーバーが TLS 対応なら暗号化し、非対応なら平文にフォールバックする（証明書は検証しない）。`required` は検証しないが、`--ssl-ca` を指定したときは CA で検証する。`verify-ca` は CA チェーンを検証する（`--ssl-ca` 必須）。`verify-identity` はチェーンとホスト名を検証する（`--ssl-ca` 省略時はシステムの CA。`--socket` とは併用不可）。Unix ソケットで `preferred` のときは TLS を使わない。クライアント証明書は対象外
  - [x] TLS を必須にした場合（`required` 以上）、TLS 非対応のサーバーへは接続せずエラーにする
  - [x] 統合テストで、TLS 接続時に `Ssl_cipher` が空でないこと、`disabled` では空であることを確認する
  - [x] README の使い方に追記する（既定が `preferred` になったことを含む）

## B-025 [done|P1] GitHub Actions で lint とテストを実行する CI を作る
- 概要: `wtnb75/mcprt` の `branch.yml` / `main.yml` と同じ構成にする。`.github/workflows/branch.yml`（`main` 以外へのプッシュ）と `main.yml`（`main` へのプッシュ）を作り、`wtnb75/actions/golang@v1`（gofmt、`go vet`、golangci-lint）と `wtnb75/actions/gotest@v1`（カバレッジ付きの `go test`、レポート）を使う。他の action は参照元と同じく SHA で固定し、`permissions` は `contents: read` から始める。`workflow_dispatch` も付ける。
- 前提: 参照元の `docker` / `deploy` ジョブは含めない（B-028、B-029 で扱う）。`wtnb75/actions/*` は参照元と同じく `@v1` で参照し、直接使う外部 action（`actions/checkout`）は SHA で固定する。Go のバージョンは `wtnb75/actions/golang` の既定（stable）に任せる。ブランチ用と `main` 用は、後で `main` 用にだけ公開処理（B-029）を足すため、別ファイルにする。
- 受け入れ条件:
  - [x] ブランチへのプッシュで、フォーマット・`go vet`・lint・単体テストが実行され、失敗したら CI が失敗する
  - [x] カバレッジの要約がジョブのサマリーに出る
  - [x] action は SHA で固定し、バージョンをコメントに残す
  - [x] README にバッジを追加する

## B-026 [done|P1] CI で MySQL 8.0 / 8.4 に対する統合テストを実行する
- 依存: B-025
- 概要: 統合テストは `MYSQLDANCE_TEST_DSN` があるときだけ動く。手元では `task test:integration` が Docker で起動するが、CI ではジョブの `services`（またはマトリクス）で MySQL 8.0 と 8.4 を起動して同じテストを実行する。参照元の `mcprt` には統合テストがないため、この部分は参照できず、新規に設計する。
- 前提: 再利用可能な workflow（`.github/workflows/integration.yml`、`workflow_call`）に統合テストのジョブを置き、`branch.yml` と `main.yml` の両方から呼ぶ。MySQL は `services` で起動し、`mysqladmin ping` のヘルスチェックで待つ。統合テストが skip されたら（`MYSQLDANCE_TEST_DSN is not set`）ジョブを失敗させる。
- 受け入れ条件:
  - [x] 8.0 と 8.4 のそれぞれで統合テストが実行される（マトリクス）
  - [x] MySQL の起動を待ってからテストを始める（ヘルスチェック）
  - [x] 統合テストが skip されていないことを CI のログで確認できる

## B-004 [done|P2] 出力をアトミックにする
- 概要: 一時的な名前で書いてからリネームし、ダンプが途中で失敗しても出力先に不完全な DB が残らないようにする（設計書 12 節）。
- 前提（仕様確定）:
  - 単位は 1 回の実行（全 DB）。全ての DB の全ファイルを出力先内の一時ファイル `.mysqldance-NNNN.tmp`（`.sql` ではないので initdb が読まない）に書き切ってから、まとめて確定する。
  - 確定は「リネームで置き換え → 今回の出力に無い古い所有ファイルを削除」の順。読み出し・書き込み・Ctrl-C の失敗は確定の前に起きるので、前回のダンプには触れない。
  - 確定中の失敗と SIGKILL・電源断は、ファイルシステムに複数ファイルのアトミックな更新がないため範囲外（README に書く）。異常終了で残った一時ファイルは次回の実行の最初に掃除する。
  - 所有ファイルが通常ファイルでない（symlink など）場合は、従来どおり何も書く前にエラーにする（確定の直前にも再検査する）。
  - 一時ファイルは rename の前に fsync する。同じ出力先への並行実行は対象外。
- 受け入れ条件:
  - [x] 書き込み中の失敗・Ctrl-C のあとも、出力先に前回のダンプが完全な形で残る
  - [x] 失敗しても一時ファイルが残らない。異常終了で残った一時ファイルは次回の実行で掃除される
  - [x] 複数 DB の途中で失敗しても、先に処理した DB の前回の出力が変わらない
  - [x] README の「Failure handling」を新しい挙動に合わせて更新する

## B-060 [done|P2] 統合テストの接続先ホストを環境変数で指定できるようにする
- 概要: `task test:integration` が MySQL に接続するホストは `HOST` 引数（既定 `127.0.0.1`）でしか変えられない。Docker デーモンが別の場所にある環境では `host.docker.internal` が要り、毎回引数を付けるか worktree ごとに設定が要る。環境変数 `MYSQLDANCE_TEST_HOST` でも指定できるようにし、ユーザー単位の設定（`~/.claude/settings.json` の `env` やシェルの profile）に置けば worktree を作り直しても引き継がれるようにする。既定値は変えない。
- 受け入れ条件:
  - [x] 優先順位は `HOST` 引数 > `MYSQLDANCE_TEST_HOST` > `127.0.0.1` にする
  - [x] `MYSQLDANCE_TEST_HOST` だけを指定して `task test:integration` が通る（8.0 と 8.4）
  - [x] Taskfile のコメントに環境変数と優先順位を書く

## B-032 [done|P2] goreleaser の設定ファイルを作る
- 概要: `mcprt` の `.goreleaser.yaml` に倣って、リポジトリ直下に `.goreleaser.yaml`（version 2）を作る。`project_name: mysqldance`、`main: ./cmd/mysqldance`、`CGO_ENABLED=0`、linux・darwin・windows × amd64・arm64、`ldflags: -s -w`、アーカイブは tar.gz（windows は zip）、`checksums.txt`、スナップショットのバージョン名（`{{ .Version }}-SNAPSHOT-{{ .ShortCommit }}`）、変更履歴（`feat:` は Features、`fix:` は Bug fixes、その他は Others。`docs:` `test:` `chore:` は除外）。`before` で `go mod tidy` を実行する。バージョンを表示するため、`main` に `version` / `commit` の変数と `--version` を追加し、`ldflags` の `-X main.version=...` で渡す（現状は無い）。コンテナイメージ（`dockers_v2`）は B-028 で決めるので、この項目には入れない。
- 前提: `--version` は stdout に `mysqldance <version> (commit <commit>)` を 1 行出して終了コード 0 で終わる。ldflags を渡さないビルドの既定値は `dev` と `unknown`。
- 受け入れ条件:
  - [x] `goreleaser check` が通る
  - [x] `goreleaser build --snapshot --clean` で、全ての OS・アーキテクチャのバイナリが `dist/` にできる
  - [x] `mysqldance --version` で、ビルド時に渡したバージョンとコミットが表示される
  - [x] ローカルで試せるよう、`Taskfile.yml` に `task snapshot`（`goreleaser build --snapshot --clean`）を追加する

## B-027 [done|P2] タグでリリースする workflow を作る
- 概要: `mcprt` の `tag.yml` に倣う。`v*` タグのプッシュで `goreleaser release --clean` を実行し、B-032 の設定に従ってバイナリ、チェックサム、変更履歴を GitHub Releases に出す。`permissions` は `contents: write` が必要。`fetch-depth: 0` で履歴を全部取る（変更履歴の生成に必要）。他の action は SHA で固定する。
- 前提: Docker 関連の step（buildx、ghcr.io へのログイン）と `packages: write` は B-028 で決めるので入れない。Go のバージョンは `go.mod` から取る（`actions/setup-go` の `go-version-file`）。goreleaser は `goreleaser/goreleaser-action` で、バージョンを `2.17.0` に固定する。テストタグ `v0.0.0-test` を push して Releases に 6 つのアーカイブと `checksums.txt` が出ること、チェックサムが一致することを確かめ、タグとリリースを削除した。
- 受け入れ条件:
  - [x] `v*` タグのプッシュで Releases にバイナリとチェックサムが出る
  - [x] テスト用のタグ（例: `v0.0.0-test`）で動作を確認してから、そのタグとリリースを削除する

## B-040 [done|P2] MySQL 5.7 に対応する
- 依存: B-039
- 概要: B-039 の調査結果（設計書 11 節）に基づき、5.7 向けの `dialect`（`mysql57`。`mysql8` を元にした差し替え）を足して `dialectFor` の「8.0 以上が必要」のエラーを 5.7 まで緩める。確認した版は 5.7.44 だけ。5.6 以前は対象外のまま拒否する。
- 受け入れ条件:
  - [x] `viewDeps` を `viewDefinitions` にして、5.7 の実機（5.7.44）で `VIEW_TABLE_USAGE` なしにビューの依存順が取れる（2 段のビューで確認する）
  - [x] セッション設定から `show_create_table_verbosity` を外し、5.7.21 以前（`Unknown system variable`）でもスナップショットを開始できる（5.7.21 と 5.7.44 で確認する）
  - [x] `TIMESTAMP` / `DATETIME` の `DEFAULT CURRENT_TIMESTAMP[(n)]` を式として読む（5.7 は `EXTRA` に `DEFAULT_GENERATED` が付かない）。`--target sqlite` / `postgres` の出力が、8.4 と同じ `DEFAULT CURRENT_TIMESTAMP` になる。`varchar` の `DEFAULT 'CURRENT_TIMESTAMP'` は文字列のまま
  - [x] 外部キーの `ON UPDATE` / `ON DELETE` を書かない場合の規則（5.7 は `RESTRICT`、8.x は `NO ACTION`）は、**揃えずにサーバーの報告をそのまま出す**（決定。どちらも SQLite と PostgreSQL でロードできる）。5.7 の `RESTRICT` が `--target sqlite` / `postgres` の出力にそのまま出るテストと、「同じスキーマでも版で出力が変わる」旨の README の記述を足す
  - [x] 5.7.44 の実機で統合テストが通る（`task test:integration:mysql57` のような専用タスクで実行する。イメージは amd64 のみなので `--platform linux/amd64` で起動する。`task test:integration` の既定には含めない）。MySQL 固有のテキストを前提にするテストは、テスト専用の判定で skip する（MariaDB と同じ）
  - [x] 5.7 の出力（`--target mysql`）を新しい 5.7 と 8.4 に復元して、行数と値が一致する往復テストがある
  - [x] `dialect` 以外のコードにバージョン固有の分岐を入れない
  - [x] README の対象バージョンと、5.7 の注意（`int(11)` や `latin1` など書式がそのまま出ること、確認した版が 5.7.44 だけであること）を更新する
