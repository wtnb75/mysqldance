superpowers:verification-before-completion に従い、最終の差分で次の「チェック」を全て実行し、出力を確認する。

- **`task check`**: `go fmt`、`go fix`、`golangci-lint`、単体テストを順に実行する。`go fmt` と `go fix` はファイルを書き換えるので、実行後に `git diff` で意図しない変更がないか見る。失敗したら、直すまで次に進まない。
  - この項目と関係のないファイルが書き換わっていたら、**戻さない**（戻しても次の `task check` でまた出る）。取り込み漏れとして、別の commit（`chore: apply go fmt / go fix ...`）にする。PR の本文の「概要」に 1 行書く。
- **`task test:integration`**: Docker で MySQL 8.0 と 8.4（と PostgreSQL 17）を起動して統合テストを実行する。`internal/source`・`internal/dump`・`internal/datafile`・`internal/schemafile`・`internal/sqlvalue`・`internal/writer`・`internal/extsort` を変えたとき、または受け入れ条件が実機の MySQL での確認を求めているときは、**必ず実行する**。
- **他のサーバー向けの統合テスト**: `task test:integration` には含まれない。次の変更をしたときは、該当するものも実行する（詳しい条件と変数は `Taskfile.yml` のコメント）。

  | 変えたもの | タスク |
  |---|---|
  | PostgreSQL 向けのライタ（`internal/writer/postgres*.go`） | `task test:integration:postgres` |
  | 方言（`internal/source/dialect.go`）、セッションの設定、カタログのクエリ | `task test:integration:mariadb`、`task test:integration:mysql57`、`task test:integration:mysql56` |

- 統合テストは `MYSQLDANCE_TEST_DSN` が無いと skip される。`task test` の結果だけで統合テストが通ったとは言わない。

あわせて、受け入れ条件を 1 つずつ満たしているか確かめる。
