ブランチを用意する。形式は `backlog/B-NNN-<slug>`（例: `backlog/B-012-tls-support`）。`<slug>` は項目の内容を表す英語の小文字・ハイフン区切りで、40文字以内。

先に `git fetch origin main` を実行する。ローカルの `main` は古いことがあるので、**ブランチの起点は常に `origin/main`** とし、ローカルの `main` は使わない。

1. **着手済みの確認**: 同じ ID のブランチが既にあれば（`git branch -a --list '*B-NNN*'`、必要なら `gh pr list --search 'B-NNN'`）、別の作業が進んでいる。新しく作らず、止まって報告する。
2. **現在のブランチが、今回の項目と同じ ID の `backlog/B-NNN-` で始まる場合**: そのまま使う。`origin/main` に新しい commit があれば、PR の前（「origin/main に追従する」）に取り込む。
3. **現在のブランチが `main` の場合**: `git switch -c backlog/B-NNN-<slug> origin/main` で作って切り替える。
4. **それ以外で、まだ push していないブランチ**（worktree の自動名など）: `git branch -m` で改名する。起点が古ければ、`origin/main` を取り込む。
5. **それ以外で、push 済みのブランチ**（別項目のブランチを含む。マージ済みでも同じ）: 改名せず、`git switch -c backlog/B-NNN-<slug> origin/main` で新しいブランチを作る。

用意した直後に `task fmt` を実行し、`git status` に差分が出たら、この項目と関係のない整形・fix の取り込み漏れ。実装を始める前に、別の commit（`chore: apply go fmt / go fix ...`）にする。
