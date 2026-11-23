---
name: refine-backlog
description: Use when the user wants to confirm, refine, or finalize the spec of backlog items before implementing them, or to make items ready (仕様を確定, 仕様を詰める, バックログを詰める, ready にする, 次に実装する項目を準備).
allowed-tools: Bash(uv run .claude/scripts/flow.py:*)
---

# refine-backlog

`BACKLOG.md` の `todo` の項目を調べ、ユーザーに聞いて仕様を確定し、`ready` にする。`ready` の項目は、`impl-backlog` が質問なしで PR の作成まで進める。ここでは**実装しない**（コードを変えない）。項目の形式は `mgmt-backlog` に従う。

## 進め方

手順は `flow.toml` に定義してある。次を繰り返す。

1. `uv run .claude/scripts/flow.py start .claude/skills/refine-backlog/flow.toml` を実行する。
2. 出力の指示に従って作業し、出力の最後に示される `next` コマンドを実行する（分岐があるときは `--result <ラベル>`、承認が要る状態では `--approval "<ユーザーの答え>"` を付ける）。
3. 出力が `Flow complete.` で終わるまで繰り返す。

`.claude/scripts/flow.py` は deskil の flow エンジンのコピー（全 skill 共通）。更新は `task flow:sync`。

## 背景

- `ready` の項目は、`impl-backlog` が着手時に前提を今のコードで確かめ直す。そのため、仕様の前提は関数名やファイル名などの確かめられる形で書く。
- 台帳 `docs/review-decisions.md` と `docs/backlog-done.md` は大きいので、全文は読まない。`rg` で必要な項目だけを読む。
- 項目の大きさの基準（1 PR に収まるか）は `mgmt-backlog` の「項目の大きさ」に従う。
