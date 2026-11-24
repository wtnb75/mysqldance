---
name: impl-backlog
description: Use when the user asks to implement, work on, start, or pick up an item from the backlog (バックログを実装, B-001をやって, 次のバックログ, BACKLOG.md の項目に着手).
allowed-tools: Bash(deskil --version), Bash(deskil flow:*)
---

Before anything else, run `deskil --version`. If the command is not found, tell the user to run `uv tool install git+https://github.com/wtnb75/deskil` and stop.

# impl-backlog

`BACKLOG.md` の項目を1つ選び、PR の作成まで自動で進める。**`ready` の項目は、質問なしで最後まで進める**。`todo` の項目は、先に仕様の確定だけをユーザーと行う。項目の形式・ID規則は `mgmt-backlog` に従う。

**REQUIRED SUB-SKILL:** 実装には superpowers:test-driven-development を使う。

## 進め方

手順は `flow.toml`（と `prompts/`）に定義してある。次を繰り返す。

1. `deskil flow start .claude/skills/impl-backlog/flow.toml` を実行する。
2. 出力の指示に従って作業し、出力の最後に示される `next` コマンドを実行する（分岐があるときは `--result <ラベル>` を付ける）。
3. 出力が `Flow complete.` で終わるまで繰り返す。

## 背景

- 前半（対象と仕様）でユーザーに聞くのは、`todo` の項目の仕様確定と、依存先が未マージのときの選択だけ。後半（ブランチの用意から PR まで）は質問しない。
- 台帳 `docs/review-decisions.md` は、却下したレビュー指摘を残して同じ指摘を繰り返さないためのもの。全文は読まず、触るファイル名や関数名で `rg` する。
- AI が書いたコードは、もっともらしいコメントが誤った実装を正当化していることが多い。レビューも自分の判断も、コメントを書いた人の主張として読む。
- 統合テストは `MYSQLDANCE_TEST_DSN` が無いと skip される。`task test` の結果だけで統合テストが通ったとは言わない。
- ブランチの起点は常に `origin/main`（ローカルの `main` は古いことがある）。
