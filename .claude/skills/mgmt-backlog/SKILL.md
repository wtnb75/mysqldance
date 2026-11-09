---
name: mgmt-backlog
description: Use when the user wants to add, remove, update, reprioritize, reword, or list items in the project backlog (バックログ, BACKLOG.md, 積む, 追加, 削除, 更新, 優先度, ステータス変更), or mentions an idea/task to "put on the backlog".
---

# mgmt-backlog

リポジトリ直下の `BACKLOG.md` 1ファイルでバックログを管理する。実装は `impl-backlog` の担当。ここでは**実装しない**。

## ファイル形式

```markdown
# Backlog

## B-003 [todo|P2] 項目のタイトル
- 依存: B-001, B-002
- 概要: 何を・なぜ（1〜3行）
- 受け入れ条件:
  - [ ] 検証可能な条件

# Done

## B-001 [done|P1] 完了した項目のタイトル
- 概要: ...
- 受け入れ条件:
  - [x] ...
```

| 要素 | 値 |
|---|---|
| ID | `B-NNN`（3桁以上ゼロ埋め）。**ファイル全体（`# Done` 含む）の最大ID+1**。削除済みIDは再利用しない |
| status | `todo` / `done`（実装中の状態は持たない。`impl-backlog` が `todo` → `done` を同じPRで行う） |
| priority | `P1`（最優先）/ `P2` / `P3`。文字列ソートでそのまま優先度順になる |

- `- 依存: B-001, B-002` は任意の1行（カンマ区切り、無ければ行ごと省く）。依存先がすべて `# Done` 節にあれば着手可能、それ以外は blocked。存在しない ID への依存も blocked 扱い。
- 項目は見出し1行 `## B-NNN [status|priority] タイトル` で完結させる。メタ情報はこの行だけに置く。
- `# Backlog` 節には `todo`、`# Done` 節には `done` だけを置く。節内の並び順に決まりはない（追記は節の末尾）。
- ファイルが無ければ `# Backlog` と `# Done` の見出しだけで新規作成する。

## 操作

絞り込みは見出し行への `rg` で行う（例: `rg '^## B-.*\[todo\|P1\]' BACKLOG.md`）。

- **追加**: `rg -o '^## B-[0-9]+' BACKLOG.md` で最大IDを確認 → 次のIDで `# Backlog` 節の末尾に追記。status は `todo`。priority や受け入れ条件がユーザー発言から不明なら、`P2` と仮置きして「仮置き」と報告する（質問で止めない）。
- **依存の設定（追加・更新時）**: 依存先の ID が `BACKLOG.md` に存在すること、自分自身や循環（依存先を辿ると自分に戻る）でないことを確認してから書く。違反するならユーザーに伝えて書かない。
- **更新**: 対象IDのブロックだけを Edit で書き換える。status・priority の変更は見出し行のみ書き換える。
- **完了**: 見出しを `[done|Pn]` にし、ブロックを `# Done` 節の末尾へ移す。削除ではない。
- **削除**: 対象IDのブロックを丸ごと消す。消す前に `rg '^- 依存:.*B-NNN' BACKLOG.md` で依存している項目を探し、あれば一覧を示して確認する（消すと依存元は永久に blocked になるため、必要なら依存元の「依存」行も一緒に直す）。
- **集計**: 件数だけなら `task backlog:stats`（priority × status の表）。着手可能な候補と blocked は `task backlog:next`。
- **一覧**: ID・status・priority・タイトルを表で返す。表は priority 昇順 → ID 昇順に並べる。デフォルトは `# Done` を除外。

## 守ること

- 対象は ID またはタイトルで特定する。曖昧（複数ヒット）なら候補を示して確認する。
- 変更後は変更したブロックの内容を日本語で要約して報告する。
- 変更は `BACKLOG.md` のみ。commit はユーザーが求めたときだけ。
- 別の worktree と並行して追加すると ID が重複しうる。マージ後に重複を見つけたら、後から入った側を採番し直す。
