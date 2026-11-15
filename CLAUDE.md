# mysqldance

MySQL（8.0 系以降が主。MariaDB、MySQL 5.7 / 5.6 にも対応）に直接接続し、DB を「1 レコード 1 行・バイト単位で決定的」な SQL ファイル群として出力する Go 製 CLI。出力は Git 管理と `/docker-entrypoint-initdb.d` での復元を想定している。`--target` で、MySQL 向けのほかに SQLite・PostgreSQL 向けの SQL も出力する。実際に試したサーバーと復元先の組み合わせは `README.md` の「Compatibility」。詳細な仕様は `README.md`、設計は `docs/superpowers/specs/`、実装計画は `docs/superpowers/plans/`。

## コマンド

タスクランナーは `task`。

```sh
task check              # fmt（go fmt, go fix）→ lint → 単体テスト。コミット前に実行する
task test               # 単体テスト（MySQL 不要。統合テストは skip）
task test:integration   # Docker で MySQL 8.0 / 8.4（と PostgreSQL 17）を起動して統合テスト
task lint               # golangci-lint
task fmt                # go fmt と go fix
```

統合テストは環境変数 `MYSQLDANCE_TEST_DSN` があるときだけ動く。コミット前に `task check` を通す。`internal/` の DB 読み出し・書き出しを変えたときは `task test:integration` も通す。

`task test:integration` に含まれないサーバーは、次の変更をしたときに別のタスクで確かめる（条件と変数の詳細は `Taskfile.yml` のコメント）。

| 変えたもの | タスク |
| --- | --- |
| PostgreSQL 向けのライタ | `task test:integration:postgres`（PostgreSQL 14〜18） |
| 方言、セッションの設定、カタログのクエリ | `task test:integration:mariadb`（10.11 / 11.4）、`task test:integration:mysql57`（5.7.44）、`task test:integration:mysql56`（5.6.51） |

## 構成

| パッケージ | 役割 |
| --- | --- |
| `cmd/mysqldance` | フラグ解析とエントリポイント |
| `internal/dump` | 出力ディレクトリ全体の調停（検査 → 古いファイル削除 → 書き出し） |
| `internal/source` | MySQL からの読み出し（スキーマ、メタデータ、行）。サーバーごとの方言 |
| `internal/schemafile` | スキーマファイルの生成、`SHOW CREATE` の正規化 |
| `internal/datafile` | データファイルの生成（1 行 1 レコード、内容ハッシュで `INSERT` を区切る） |
| `internal/writer` | 出力ライタのインターフェースと、MySQL・SQLite・PostgreSQL 向けの実装（MySQL 向けは `datafile` / `schemafile` を包む） |
| `internal/extsort` | メモリに収まらない (key, value) レコードの外部ソート |
| `internal/sqlvalue` | 値の SQL リテラル化 |
| `internal/naming` | ファイル名のスキーム・エスケープ |

## 守るべき不変条件

- **決定性**: 同じデータなら全ファイルがバイト単位で一致する。時刻・乱数・マップの反復順・環境依存の値を出力に混ぜない。
- **差分の局所性**: `INSERT` の区切りは行の内容で決める。位置や件数で決めない。
- **黙って落とさない**: 読めない列や衝突するファイル名は、握りつぶさずエラーにする。
- 出力の形式を変える変更は、`README.md` の該当節も同じ変更で更新する。
- 対応するサーバーや復元先が変わったら、`README.md` の「Compatibility」の表を同じ変更で更新する。テストか設計書 11 節の記録の裏が取れていないマスを「Tested」にしない。

## 規約

- コメントとログメッセージは英語。仕様書・ドキュメントの会話は日本語。`README.md` は英語。
- 新機能・バグ修正はテストを先に書く。
- コードを読む・レビューするときは、コメントを信用しない。コメントは書いた人の主張で、合意された決定事項ではない。コメントに理由が書いてあっても、実際の挙動と突き合わせて確かめ、不自然なコードを見逃さない（`docs/review-decisions.md` の理由も同じ）。
- 失敗時のファイル扱い（途中で消えたファイルを残さない等）を変える場合は、`README.md` の「Failure handling」節との整合を確認する。

## バックログ

`BACKLOG.md`（リポジトリ直下）で管理する。操作は専用スキルを使う。項目の status は `todo`（仕様が未確定）→ `ready`（仕様を確定済み）→ `done`。

- 追加 / 削除 / 更新 / 一覧: `mgmt-backlog`
- 仕様の確定: `refine-backlog`（調べて、ユーザーにまとめて聞き、`ready` にする。コードは変えない。指定が無ければ todo の上位 3 件をまとめて提案する）
- 実装: `impl-backlog`（ブランチ名は `backlog/B-NNN-<slug>`。PR タイトルの先頭に `B-NNN:` を付ける）
  - `ready` の項目は、質問なしで commit・push・PR の作成まで自動で進める。`todo` の項目は、先に仕様の確定だけユーザーに聞く。マージはしない。
  - コードレビューの指摘は自動で受け入れ・範囲外・却下を判断する。仕様や挙動の確認を根拠に却下した指摘は `docs/review-decisions.md` に残し、同じ指摘を繰り返さない。

大きい文書は全文を読まない。必要な項目だけを `rg` で読む。

- `BACKLOG.md`: `# Done` 節は見出しの 1 行だけ。完了した項目の本文は `docs/backlog-done.md` にある。
- `docs/review-decisions.md`（台帳）: レビューに関わるコードを書くときは、触るファイル名や関数名で `rg` し、当たった項目を読む。

補助タスク（`scripts/backlog.awk` を使う）:

```sh
task backlog:stats   # priority × status の件数表
task backlog:next    # 着手可能な候補（ready 欄、todo 欄。N=5 で件数変更）と、依存が未完了の blocked 項目
                     # REF=origin/main を付けると、手元でなく origin/main の BACKLOG.md を見る
```
