---
name: impl-backlog
description: Use when the user asks to implement, work on, start, or pick up an item from the backlog (バックログを実装, B-001をやって, 次のバックログ, BACKLOG.md の項目に着手).
---

# impl-backlog

`BACKLOG.md` の項目を1つ選んで実装する。項目の形式・ID規則は `mgmt-backlog` に従う。

**REQUIRED SUB-SKILL:** 実装には superpowers:test-driven-development を使う。項目が曖昧・大きい場合は先に superpowers:brainstorming。

## 手順

1. **対象を決める**: ユーザー指定のID/タイトル。指定が無ければ `task backlog:next` を実行し、候補の先頭（最も優先度が高く ID が最小）を提案して、了承を得てから着手する。ユーザーが指定した項目が `task backlog:next` の blocked 欄にあれば、着手せずに待っている依存先を伝え、先に依存先を実装することを提案する。`# Done` 節の項目は対象外。
2. **読む**: 該当ブロックの概要と受け入れ条件を読む。受け入れ条件が空・検証不能なら、実装前にユーザーと条件を決めて `BACKLOG.md` に書く（`mgmt-backlog` の更新）。
3. **ブランチを用意する**: 下の「ブランチ名」に従う。`BACKLOG.md` は実装中の状態を持たないので、着手済みかどうかはブランチで判断する。
4. **実装**: TDDで、受け入れ条件を1つずつテストに落として満たす。プロジェクトのテスト・lint（`task` など）を実行する。
5. **検証**: superpowers:verification-before-completion に従い、テスト出力を確認してから完了を主張する。
6. **完了を記録**: 実装と同じ変更の中で、受け入れ条件のチェックを `[x]` にし、全て満たしたときだけ `done` にして、ブロックを `# Done` 節へ移す（`mgmt-backlog` の「完了」）。`BACKLOG.md` に実装中の中間状態は書かない。満たせないものがあれば `todo` のまま残し、理由を報告する。
7. **報告**: 変更内容・テスト結果・未達の条件を日本語で報告する。commit/PR はユーザーが求めたときだけ。

## ブランチ名

形式は `backlog/B-NNN-<slug>`（例: `backlog/B-012-tls-support`）。`<slug>` は項目の内容を表す英語の小文字・ハイフン区切りで、40文字以内。

1. **着手済みの確認**: 同じ ID のブランチが既にあれば（`git branch -a --list '*B-NNN*'`、必要なら `gh pr list --search 'B-NNN'`）、別の作業が進んでいる。新しく作らず、ユーザーに伝えて続けるか止めるかを確認する。
2. **現在のブランチが `backlog/B-NNN-` で始まる場合**: そのまま使う。
3. **`main` の場合**: `backlog/B-NNN-<slug>` を作って切り替える。
4. **それ以外で、まだ push していないブランチ**（worktree の自動名など）: `git branch -m` で改名する。
5. **それ以外で、push 済みのブランチ**: 改名せず、新しいブランチを作るかをユーザーに確認する。

PR を作るときは、タイトルの先頭に `B-NNN:`（または `feat(B-NNN):` のようにスコープに入れる）を付ける。複数のコミットで項目を扱うときも、ブランチ名で対応を取る。

## 守ること

- 1回の依頼で扱う項目は1つ。複数指定されたら1つずつ順に完了させる。
- 実装中に見つけた別件は実装せず、`mgmt-backlog` で新規項目として追加する。
- `BACKLOG.md` の変更は対象項目のブロックだけ。
