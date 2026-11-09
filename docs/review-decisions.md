# Review decisions

`impl-backlog` のコードレビューで**却下した指摘**の台帳。同じ指摘が再び出たとき、再検討せずにこの台帳で判断するために残す。受け入れた指摘（コードを直したもの）は書かない。

## 書き方

```markdown
## R-NNN 指摘の要旨（1行）
- 対象: path/to/file.go の関数名やパターン
- 指摘: レビューが何を問題としたか
- 却下の理由: なぜ問題ではないか。再現テストや仕様（設計書の節、確定した仕様）への参照を含める
- 初出: B-NNN（PR の番号）
- 再出: 回数（同じ指摘が再び出るたびに増やす）
```

- ID は `R-NNN`（3桁以上、最大 ID + 1）。
- 対象のコードが変わって理由が当てはまらなくなったら、項目を見直して更新または削除する。
- 再出が 1 以上の項目は、指摘された箇所に理由のコメント（英語）が足りていない。コメントを足す。

## 却下した指摘

## R-001 既定の `--ssl-mode=preferred` は証明書を検証せず、ダウングレードや既存利用の失敗がありうる
- 対象: internal/source/tls.go の tlsConfig（preferred）
- 指摘: 既定が平文から証明書検証なしの TLS に変わり、能動的な中間者にダウングレードされる。TLS 1.2 未満のサーバーでは失敗する。
- 却下の理由: 既定を `preferred` にするのは B-012 の仕様確定でユーザーが選んだ（mysql クライアントと同じ）。ダウングレードされうることは README の TLS 節に明記している（盗聴対策のみ）。MySQL 8.0 / 8.4 は TLS 1.2 以上に対応し、統合テストで確認済み。検証が要る利用者は `verify-ca` / `verify-identity` を選ぶ。
- 初出: B-012
- 再出: 0

## R-002 `required` に `--ssl-ca` を付けると `verify-ca` と同じになる
- 対象: internal/source/tls.go の tlsConfig（`mode == SSLVerifyCA || roots != nil`）
- 指摘: `required` + CA が `verify-ca` の別名になり、モードを区別できない。
- 却下の理由: 確定した仕様そのもの（「`required` は検証しないが、`--ssl-ca` を指定したときは CA で検証する」）。CA を渡したのに検証しない方が「黙って落とす」ことになる。README に記載済み。
- 初出: B-012
- 再出: 0

## R-003 `verifyChain` のエラーに文脈が付かない
- 対象: internal/source/tls.go の verifyChain
- 指摘: 検証失敗のエラーが x509 の素のメッセージで、どの設定で失敗したか分かりにくい。期限切れや中間 CA のテストがない。
- 却下の理由: 失敗はエラーとして必ず返り、握りつぶされない（不変条件は満たす）。メッセージの好みの問題。検証ロジックは標準の `x509.Verify` で、信頼できる CA / 別の CA / 証明書なしをテスト済み。
- 初出: B-012
- 再出: 0

## R-004 `--ssl-ca` の不備が `Open` まで分からず、処理開始後に失敗する
- 対象: internal/source/source.go の Open / internal/dump/dump.go の dumpDB
- 指摘: CA ファイルの不備が、dump が作業を始めた後に見つかる。
- 却下の理由: `dumpDB` は最初に `source.Open` を呼び、古いファイルの削除はその後。設定の誤りは 1 つ目の DB の `Open` で、削除の前に失敗する。モードと CA の組み合わせはフラグ解析時に `ValidateSSL` で先に弾いている。
- 初出: B-012
- 再出: 1（CA を DB ごとに読み直す点として再出。source.go の Open にコメントを足した）

## R-005 `--ssl-mode` が大文字や `VERIFY_CA` を受け付けない
- 対象: cmd/mysqldance/main.go の `--ssl-mode`
- 指摘: mysql クライアントは大文字・アンダースコアを受け付けるが、このツールは小文字のハイフン形式のみ。
- 却下の理由: 不正な値は使い方のエラーとして明示的に拒否され、黙って別の動作にはならない。好みの問題で、`CLAUDE.md` に規約はない。README は小文字の値を列挙している。
- 初出: B-012
- 再出: 0

## R-006 `preferred` の TLS 1.2 下限で、古い TLS しか話せないサーバーへの接続が失敗する
- 対象: internal/source/tls.go の tlsConfig（`MinVersion: tls.VersionTLS12`）
- 指摘: TLS 1.0/1.1 しか使えないサーバーで、以前は平文で繋がったものがハンドシェイク失敗になる。
- 却下の理由: 対象は MySQL 8.0 系以降（CLAUDE.md）で、8.0.28 以降は TLS 1.0/1.1 が無効。Go のクライアント既定の下限も TLS 1.2 で、明示は既定と同じ。失敗は黙って落とさずエラーになり、`--ssl-mode disabled` で従来の動作に戻せる（README に記載）。
- 初出: B-012
- 再出: 0

## R-007 カタログ（DDL）はスナップショットの対象外なので、複数 DB では不整合の窓が広がる
- 対象: internal/source/source.go の Use
- 指摘: 行は共有スナップショットから読むが、`information_schema` と `SHOW CREATE` は最新の定義を返す。後ろの DB の DDL が、長い読み出しの間に混ざりうる。
- 却下の理由: MVCC がカタログに効かないのは MySQL の仕様で、1 DB のときから同じ（B-003 の確定した前提に明記。README の Reads 節にも書いた）。DDL を止めるには `FLUSH TABLES WITH READ LOCK` などが要り、B-005 の範囲。
- 初出: B-003
- 再出: 0

## R-008 `Run` が先頭の DB だけ `Open` し、接続エラーが `database "X"` 付きになる
- 対象: internal/dump/dump.go の Run
- 指摘: 接続や TLS のエラーに DB 名が付いて紛らわしい。先頭の DB の特別扱いが不格好。
- 却下の理由: エラーは握りつぶされず、従来も `Run` が DB 名を付けていた（挙動は変わらない）。`Open` が最初の DB の存在確認まで行うのは既存の契約。好みの問題。
- 初出: B-003
- 再出: 0

## R-009 `Use` の失敗後の状態、大文字小文字の照合、`start` 失敗時の `sql.DB` の解放
- 対象: internal/source/source.go の Use / start
- 指摘: `Use` が失敗しても前の DB のままで、呼び出し側が無視すると別の DB を読む。SCHEMATA の完全一致は `lower_case_table_names` で不一致になりうる。`start` が `sdb` を閉じない。
- 却下の理由: `Run` は `Use` のエラーで中止し、続行する経路がない（推測のシナリオ）。完全一致の照合は B-003 以前からの挙動で、不一致は「見つからない」エラーになり黙って落とさない。`start` の失敗時は `Open` が `sdb.Close()` を呼ぶ（source.go の Open）。
- 初出: B-003
- 再出: 0

## R-010 `TestUseReadsSameSnapshot` が遅延スナップショットへの退行を捕まえない
- 対象: internal/source/use_test.go
- 指摘: `BEGIN` に変わっても、先に読んだ DB の分しか検証できない。
- 却下の理由: テストは 2 つ目の DB に、スナップショット開始後・最初の読み出し前に行を足して、見えないことを確かめている。2 つ目の DB は `Use` 時点まで一度も読まれていないので、遅延スナップショットなら失敗する。
- 初出: B-003
- 再出: 0

## R-011 テストの `USE` と次の `INSERT` がプール上の別の接続で動きうる
- 対象: internal/dump/dump_test.go の TestFailureInLaterDatabaseKeepsEarlierOnes
- 指摘: `e.exec` が接続プールを使うので、`USE` の状態が次の文に引き継がれない。
- 却下の理由: `e.admin` は `newEnv` で `db.Conn` により固定した 1 本の接続で、プールではない。既存のテストも同じ書き方をしている。
- 初出: B-004
- 再出: 0

## R-012 同じ出力先への並行実行で、一時ファイルの掃除と名前が衝突する
- 対象: internal/dump/dump.go の sweepTemps / stage.create
- 指摘: 実行 A の一時ファイルを実行 B が掃除する。名前は実行ごとに一意でない。
- 却下の理由: 同じ出力先への並行実行は対象外（ロックは設けない）。確定する前提が壊れるのは、並行実行がなくても出力先を他のプロセスが書き換えるときと同じ。README の Failure handling に「同時に実行しない」と明記した。
- 初出: B-004
- 再出: 0

## R-013 一時ファイルの名前に symlink やディレクトリがあると掃除されず、作成が失敗する
- 対象: internal/dump/dump.go の sweepTemps
- 指摘: 通常ファイル以外の `.mysqldance-NNNN.tmp` は消されず、`O_EXCL` の作成が「file exists」で失敗する。
- 却下の理由: 失敗はエラーとして返り、黙って落とさない。symlink を辿って消す・書くことはしない（削除の安全側）。実害のある利用の筋道がない。
- 初出: B-004
- 再出: 0

## R-014 確定後の stale 削除の失敗で、新旧が混在する
- 対象: internal/dump/dump.go の Run（removeStale）
- 指摘: リネームの後に削除が失敗すると、新しいファイルと古い stale ファイルが残る。
- 却下の理由: 確定中の失敗として確定した仕様と README の Failure handling に記載済み。複数ファイルのアトミックな更新はファイルシステムにない。
- 初出: B-004
- 再出: 0

## R-015 確定中（リネーム・削除）の Ctrl-C を無視する
- 対象: internal/dump/dump.go の Run（確定の直前の ctx.Err() が最後の確認）
- 指摘: 確定の間は中断できず、ファイルが多いと長引く。
- 却下の理由: 確定を途中で止めると新旧が混在するので、止めないのが確定した仕様（BACKLOG の B-004 の前提）。確定はリネームと削除だけで、読み出しや書き込みはない。
- 初出: B-004
- 再出: 0

## R-016 ロックの要否を判断した後にテーブルが非 InnoDB になると、ダンプが失敗する（TOCTOU）
- 対象: internal/source/source.go の start（firstNonInnoDB）と Tables の `!s.locked` の検査
- 指摘: 判断とロックの間に非 InnoDB のテーブルができると、偽の失敗になる。
- 却下の理由: 失敗は必ずエラーとして返り、黙って非一貫にはならない（不変条件を満たす）。MySQL 8 のデータディクショナリはスナップショットの下で読まれるため、実際にはそのテーブルは見えない（TestNonInnoDBTableWithoutLock のコメント）。再実行か `--no-lock` で回避できる。
- 初出: B-005
- 再出: 0

## R-017 ENGINE が NULL のテーブルや DB 名の大文字小文字で、ロックの判断を外す
- 対象: internal/source/source.go の firstNonInnoDB
- 指摘: ENGINE が NULL の基本テーブルは `<>` で除かれ、IN の比較も大文字小文字に影響されうる。
- 却下の理由: 壊れたテーブルは Tables がエラーにする。DB 名の比較は `Use`（SCHEMATA の完全一致）と同じ規則で、判断と読み出しで食い違わない。失敗のシナリオを再現できない。
- 初出: B-005
- 再出: 0

## R-018 既定でロックを取るため、RELOAD 権限が無いアカウントの既存の運用が失敗する
- 対象: internal/source/source.go の start（FLUSH TABLES WITH READ LOCK）
- 指摘: 以前は警告して続行したのが、エラーになる。
- 却下の理由: B-005 の仕様確定でユーザーが選んだ（非 InnoDB があるとき自動でロックし、取れなければエラー。回避は `--no-lock`）。黙って非一貫にしない不変条件に沿う。README に記載済み。
- 初出: B-005
- 再出: 0

## R-019 `Use` で `Open` に渡していない DB を読むと、非 InnoDB のテーブルでエラーになる
- 対象: internal/source/source.go の Open の契約と Tables の検査
- 指摘: 契約（dbs に全 DB を渡す）がコメントだけで保証されていない。
- 却下の理由: 呼び出し元は dump.Run だけで、全 DB を渡している。誤用してもエラーで止まり、黙って非一貫にはならない。
- 初出: B-005
- 再出: 0

## R-020 FTWRL 後の失敗経路で、ロックを明示的に解除しない
- 対象: internal/source/source.go の start のエラー経路
- 指摘: `conn.Close()` はプールに返すだけで、ロックが残りうる。
- 却下の理由: `Open` は失敗時に `sdb.Close()` で全接続を閉じ、接続の終了でロックは解除される。`start` を使うのは `Open` と同じパッケージのテストだけ。
- 初出: B-005
- 再出: 0

## R-021 dump の TestRoundTrip がロックの取得を確かめない
- 対象: internal/dump/dump_test.go の TestRoundTrip
- 指摘: ロックが退行しても、警告が出ないことだけを見るので通る。
- 却下の理由: ロックの取得と解除は internal/source/lock_test.go（TestLockHeldForNonInnoDB ほか）が、他接続の書き込みが待たされることで確かめている。dump 層は DB の一覧を Open に渡すだけ。
- 初出: B-005
- 再出: 0
