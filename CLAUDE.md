# mysqldance

MySQL 8.0 系以降に直接接続し、DB を「1 レコード 1 行・バイト単位で決定的」な SQL ファイル群として出力する Go 製 CLI。出力は Git 管理と `/docker-entrypoint-initdb.d` での復元を想定している。詳細な仕様は `README.md`、設計は `docs/superpowers/specs/`、実装計画は `docs/superpowers/plans/`。

## コマンド

タスクランナーは `task`。

```sh
task test               # 単体テスト（MySQL 不要。統合テストは skip）
task test:integration   # Docker で MySQL 8.0 / 8.4 を起動して統合テスト
task lint               # golangci-lint
task fmt                # gofmt
```

統合テストは環境変数 `MYSQLDANCE_TEST_DSN` があるときだけ動く。コミット前に `task fmt` `task lint` `task test` を通す。

## 構成

| パッケージ | 役割 |
| --- | --- |
| `cmd/mysqldance` | フラグ解析とエントリポイント |
| `internal/dump` | 出力ディレクトリ全体の調停（検査 → 古いファイル削除 → 書き出し） |
| `internal/source` | MySQL からの読み出し（スキーマ、メタデータ、行） |
| `internal/schemafile` | スキーマファイルの生成、`SHOW CREATE` の正規化 |
| `internal/datafile` | データファイルの生成（1 行 1 レコード、内容ハッシュで `INSERT` を区切る） |
| `internal/sqlvalue` | 値の SQL リテラル化 |
| `internal/naming` | ファイル名のスキーム・エスケープ |

## 守るべき不変条件

- **決定性**: 同じデータなら全ファイルがバイト単位で一致する。時刻・乱数・マップの反復順・環境依存の値を出力に混ぜない。
- **差分の局所性**: `INSERT` の区切りは行の内容で決める。位置や件数で決めない。
- **黙って落とさない**: 読めない列や衝突するファイル名は、握りつぶさずエラーにする。
- 出力の形式を変える変更は、`README.md` の該当節も同じ変更で更新する。

## 規約

- コメントとログメッセージは英語。仕様書・ドキュメントの会話は日本語。`README.md` は英語。
- 新機能・バグ修正はテストを先に書く。
- 失敗時のファイル扱い（途中で消えたファイルを残さない等）を変える場合は、`README.md` の「Failure handling」節との整合を確認する。

## バックログ

`BACKLOG.md`（リポジトリ直下）で管理する。操作は専用スキルを使う。

- 追加 / 削除 / 更新 / 一覧: `mgmt-backlog`
- 実装: `impl-backlog`（ブランチ名は `backlog/B-NNN-<slug>`。PR タイトルの先頭に `B-NNN:` を付ける）

補助タスク（`scripts/backlog.awk` を使う）:

```sh
task backlog:stats   # priority × status の件数表
task backlog:next    # 次に選ぶ todo 候補（N=5 で件数変更）と、依存が未完了の blocked 項目
```
