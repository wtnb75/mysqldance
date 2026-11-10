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

## R-022 確定済みのダンプでも、`Close` の ROLLBACK のタイムアウトで終了コード 1 になる
- 対象: internal/source/source.go の Close / internal/dump/dump.go の Run（`snap.Close()` を errors.Join する）
- 指摘: 出力が確定した後にサーバーが止まると、ROLLBACK のタイムアウトで失敗として報告される。
- 却下の理由: `Close` のエラーを Run のエラーに含めるのは B-014 以前からの挙動（接続が切れていれば ROLLBACK は元から失敗していた）。変わったのは、待ち続けずにタイムアウトで返す点だけ。ロックやトランザクションの後始末に失敗したことを黙って成功にしない（不変条件）。READMEの「どの失敗でも終了コード 1」とも一致する。
- 初出: B-014
- 再出: 0

## R-023 SIGTERM から終了コード 1・一時ファイルの削除までを通しで確かめるテストがない
- 対象: cmd/mysqldance/signal_unix_test.go
- 指摘: テストは `notifyContext` が ctx を閉じることしか確かめていない。
- 却下の理由: SIGINT と SIGTERM は同じ ctx に流れ、その先の経路は SIGINT のときと同一。ctx の中断で古いファイルを残し一時ファイルを消すことは dump の TestCancelKeepsPreviousDump が確かめている。終了コード 1 は `run` が `dump.Run` のエラーを返すだけの分岐。経路が分かれる点（シグナルから ctx）を新しいテストで押さえた。
- 初出: B-014
- 再出: 0

## R-024 `closeTimeout` の書き換えがテスト間で競合し、MySQL なしの単体テストでは検証されない
- 対象: internal/source/close_test.go の TestCloseTimesOutWhenServerStalls
- 指摘: パッケージ変数を同期なしで書き換える。MySQL が無いと skip される。
- 却下の理由: このパッケージのテストに `t.Parallel` はなく、競合する読み手がいない。統合テストが `task test:integration` と CI で 8.0 / 8.4 に対して実行される。ハンドシェイクを偽装する単体テストは、得るものに対して重い（好みの問題）。
- 初出: B-014
- 再出: 0

## R-025 ROLLBACK の中断後、`conn.Close()` / `db.Close()` が待ち続けうる
- 対象: internal/source/source.go の Close
- 指摘: 上限は `ExecContext` にしか掛かっておらず、後続の Close が長引く可能性がある。
- 却下の理由: 推測。ドライバーは接続を閉じるときにソケットを閉じるだけで、応答を待たない。サーバーが応答しない状態を再現する統合テスト（TestCloseTimesOutWhenServerStalls）で、`Close` 全体が約 0.35 秒（上限 0.3 秒）で返ることを確認している。
- 初出: B-014
- 再出: 0

## R-026 テーブルが 1 ファイルから 2 ファイル以上になると、ファイル名が変わって全体が書き換わる
- 対象: internal/dump/dump.go の dumpDB（`naming.DataFileParts`）
- 指摘: 1 ファイルのときは番号なし、2 ファイル以上は番号付きなので、境界が 1 つ増えるとファイル名が変わる。
- 却下の理由: B-009 の仕様確定でユーザーが選んだ（「番号なし」。1 ファイルのときは従来の名前を保つ）。README の「Splitting large tables」に、成長でファイル名が変わることを明記した。
- 初出: B-009
- 再出: 0

## R-027 ファイルの大きさが幾何分布になり、上限がない（`--rows-per-file` は平均）
- 対象: internal/datafile/datafile.go の Split
- 指摘: 小さいファイルや平均の数倍のファイルができる。
- 却下の理由: 内容で区切る方式（設計書 4.2 節の INSERT と同じ。差分の局所性の不変条件）の性質で、仕様確定でも「平均行数」とした。上限を付けると、位置で区切ることになり局所性を失う。README とフラグの説明に「平均」と書いている。
- 初出: B-009
- 再出: 0

## R-028 キーのないテーブルのハッシュが `strings.Join(row, ",")` で、曖昧な連結になる
- 対象: internal/datafile/datafile.go の keyHash
- 指摘: `('a,b','c')` と `('a','b,c')` が同じ文字列になる。
- 却下の理由: ハッシュは境界の位置を決めるだけで、衝突しても出力の内容や復元には影響しない。決定性（同じデータなら同じバイト列）は保たれる。失敗のシナリオを示せない。
- 初出: B-009
- 再出: 0

## R-029 分割後のファイル名が 255 バイトを超える、または大文字小文字で衝突するのが、書き出しの途中で分かる
- 対象: internal/dump/dump.go の dumpDB（分割後の `claim`）
- 指摘: ファイル名の検査が、行を読んだ後になる。行数によって成否が変わる。
- 却下の理由: エラーとして必ず返り、確定（commit）の前なので出力先は変わらない（黙って落とさない。README の Failure handling どおり）。部分名はテーブル名のエスケープに `.` が含まれないため、他のテーブルのファイル名とは衝突しない。
- 初出: B-009
- 再出: 0

## R-030 `--rows-per-file` はメモリを減らさず、一時ファイルでディスクも使う
- 対象: internal/dump/dump.go の dumpDB / cmd/mysqldance/main.go のフラグ
- 指摘: 全行をメモリに読むのは変わらない。
- 却下の理由: この項目の範囲はファイル分割で、ストリーム化は B-044。README の Limitations に「分割してもテーブルの全行をメモリに読む」と明記した。
- 初出: B-009
- 再出: 0

## R-031 `keyColumns` が、キーの列が行に無いとき黙って行全体のハッシュに切り替える
- 対象: internal/dump/dump.go の keyColumns
- 指摘: 列名の不一致で、非キー列の更新が境界を動かしうる。
- 却下の理由: `OrderBy` と `Columns` は同じカタログから読むので、名前の不一致は起きない。行に無いのは生成列がキーに含まれるときだけで、関数のコメントに書いた。この場合も出力は決定的で、失敗にはならない（再現できない推測）。
- 初出: B-009
- 再出: 0

## R-032 `--rows-per-file 1` のような小さい値に下限や警告がない
- 対象: cmd/mysqldance/main.go の `--rows-per-file`
- 指摘: 行ごとに 1 ファイルになり、遅く大量のファイルができる。
- 却下の理由: 利用者が明示的に選ぶ値で、動作は仕様どおり。下限を決めるのは好みの問題で、CLAUDE.md に規約はない。
- 初出: B-009
- 再出: 0

## R-033 1 ファイルと複数ファイルの切り替え、部分名の衝突の統合テストがない
- 対象: internal/dump/dump_test.go の TestSplitLargeTable
- 指摘: 名前の切り替えと衝突の経路が未検証。
- 却下の理由: 切り替えは、分割ありから分割なしへ戻して古い番号付きファイルが消えることを同じテストで確かめている（`small` と `empty_t` は番号なしのまま）。部分名の衝突は、エスケープ後の名前に `.` が含まれないため起きない（R-029）。
- 初出: B-009
- 再出: 0

## R-034 `--dir-mode` が親ディレクトリと既存ディレクトリに効かないことを警告しない
- 対象: internal/dump/dump.go の prepareOutDir
- 指摘: `-o a/b/out` では `a` と `a/b` が 0755 で作られ、既存の出力ディレクトリには何も適用されないのに、警告が出ない。
- 却下の理由: 「既存の出力ディレクトリのモードは変えない」は B-013 の受け入れ条件で、親に適用しない範囲も README の `--dir-mode` の行に明記している。警告を足すかは好みの問題で、失敗のシナリオ（データの破損・欠落、決定性）に当たらない。
- 初出: B-013
- 再出: 0

## R-035 `0` を「未指定」の印に使っている（`FileMode` / `DirMode` / `stage.mode`）
- 対象: internal/dump/dump.go の Options、stage.create の `cmp.Or(s.mode, 0o666)`
- 指摘: `0` を「権限なし」の意味で設定した呼び出し側が、黙って既定値になる。
- 却下の理由: フラグ側は `--file-mode 0` / `--dir-mode 0` を所有者の権限が足りないとして拒否する（parseMode、main_test.go の usage エラー）ので、`0` が実際に渡ることはない。内部の `Options` を使うのはこのコマンドだけで、型のコメントに `0` の意味を書いている。
- 初出: B-013
- 再出: 0

## R-036 `--file-mode 0400` のように所有者が書けないモードを受け付ける
- 対象: cmd/mysqldance/main.go の parseMode
- 指摘: 所有者が書けないファイルができ、後で `git clean` などが失敗しうる。
- 却下の理由: 利用者が明示的に選ぶ設定で、ツールの動作に影響しない。出力は一時ファイルへ書いて rename で置き換えるので、読み取り専用のファイルがあっても再実行は成功する（統合テストの TestOutputModes は 0600 で確認）。`git clean` はファイルのモードではなくディレクトリの書き込み権限で決まる。
- 初出: B-013
- 再出: 0

## R-037 `QuoteIdent` を値のリテラル化の `sqlvalue` に置くのは責務が違う（`naming` に置くべき）
- 対象: internal/sqlvalue/sqlvalue.go の QuoteIdent
- 指摘: `sqlvalue` は値のリテラル化のパッケージなので、識別子のクォートは別の場所が適切。
- 却下の理由: 失敗のシナリオがなく、好みの問題。`naming` はファイル名のスキームとエスケープで、SQL のクォートとは無関係。`sqlvalue` は「SQL の字句を作る」leaf パッケージで、`source`・`datafile`・`schemafile` の全てが既に（または問題なく）依存できる。新しいパッケージは 1 関数のために過剰。
- 初出: B-020
- 再出: 0

## R-038 B-020 の受け入れ条件「テストを 1 か所に集める」が一部しか満たされていない
- 対象: BACKLOG.md の B-020
- 指摘: 統合テストなどにバッククォートの名前のテストが残っている。
- 却下の理由: `quoteIdent` 自体のテストは `sqlvalue.TestQuoteIdent` に集めた（`rg quoteIdent` の結果は 0 件）。`datafile`・`schemafile`・`dump` に残るバッククォートのテストは、ヘッダや DDL の出力全体を確かめるもので、関数の単体テストではない。
- 初出: B-020
- 再出: 0

## R-039 `Use` の後に前の DB の `TableInfo` で `Rows` を呼ぶと、古い列・型で読む
- 対象: internal/source/source.go の Rows / Tables
- 指摘: `Tables` と `Rows` が呼び出し順の約束に依存し、DB を切り替えると `TableInfo` が古くなる。
- 却下の理由: `Rows` の列は B-021 より前から `TableInfo.Columns` を使っていて、新しく生じた危険ではない。呼び出し元は `dump.Run` だけで、DB ごとに `Tables` の直後に `Rows` を呼ぶ。B-021 は `Snapshot` の隠れた状態を除くことが目的で、値は戻り値と `TableInfo` で渡すようになった。`Rows` のコメントに「`t` は現在の DB の `Tables` の結果」と書いた。
- 初出: B-021
- 再出: 0

## R-040 `TestUseResetsPerDatabaseState` が、キャッシュの漏れという目的を失った
- 対象: internal/source/use_test.go の TestUseResetsPerDatabaseState
- 指摘: キャッシュが無くなったので、テストは `Use` が DB を切り替えることしか確かめない。
- 却下の理由: 好みの問題で、失敗のシナリオがない。同名のテーブルを持つ 2 つの DB を切り替えて、それぞれの内容が読めることを確かめる価値は残る。コメントは実態に合わせて直した。
- 初出: B-021
- 再出: 0

## R-041 テストのヘルパーが `Tables` を何度も呼び、カタログの読み出しを繰り返す
- 対象: internal/source/itest_test.go の tableByName / warningsOf
- 指摘: ヘルパーごとに `Tables` が全テーブルの `SHOW CREATE` などを実行する。
- 却下の理由: 増えるのはテストの実行時間だけで、本番のコードは DB ごとに `Tables` を 1 回しか呼ばない（dump.go）。ヘルパーを共通化する利点が小さい（好みの問題）。
- 初出: B-021
- 再出: 0

## R-042 goreleaser の `before` で `go mod tidy` を実行すると、リポジトリを書き換えうる
- 対象: .goreleaser.yaml の `before.hooks`
- 指摘: go.mod / go.sum が整っていないと追跡中のファイルが変わり、リリースが git の dirty 検査で失敗する。ネットワークも要る。
- 却下の理由: B-032 の仕様で `before` に `go mod tidy` を置くと決まっている（`mcprt` に倣う）。失敗するときはリリースが止まるだけで、黙って壊れた成果物は出ない。整っていない状態は CI での検査で防ぐもので、この項目の範囲外。
- 初出: B-032
- 再出: 0

## R-043 アーカイブに LICENSE が入らない可能性がある
- 対象: .goreleaser.yaml の `archives`
- 指摘: `files` を指定しておらず、LICENSE が無いリポジトリでは、ライセンスなしで配布される。
- 却下の理由: リポジトリに LICENSE が無いことと、ライセンスの方針は別の話で、設定ファイルの欠陥ではない。goreleaser の既定の glob は LICENSE* と README* を含み、LICENSE を足せば自動で入る。失敗のシナリオを示せない（推測）。
- 初出: B-032
- 再出: 0

## R-044 型付きの行とリテラルの行が同時にメモリにあり、ピークが増える
- 対象: internal/dump/dump.go の dumpDB（`sqlvalue.Literals` の呼び出し）
- 指摘: `rows`（`[][]sqlvalue.Value`）と `literals`（`[][]string`）が表ごとに同時に生きる。
- 却下の理由: `Rows` は表全体をメモリに載せる設計で、これは MVP から変わらない。表全体を載せない行ストリーム化は B-044（キーのあるテーブル）と B-045（キーのないテーブル）の範囲で、B-033 の範囲外。出力のバイト列は変わらず、失敗のシナリオ（具体的な OOM の閾値）も示せない。
- 初出: B-033
- 再出: 1（B-034: `DataParts` の `Part` がリテラルを持つ。`internal/writer/mysql.go` の `DataParts` にコメントを足した）

## R-045 キーのない表で、並べ替えキーの作成と dump で、リテラル化が 2 回走る
- 対象: internal/source/source.go の Rows（キーなしの並べ替え）
- 指摘: 並べ替えのキーを作るときと、dump が書き出す前に、同じセルを 2 回リテラル化する。
- 却下の理由: 処理が 2 倍になるだけで、出力・決定性・差分の局所性に影響しない。B-033 の目的は、型付きの値を返し、リテラル化を `sqlvalue` に残すこと。キーのない表の外部ソート（B-045）で並べ替えの方式ごと変わるので、ここでキャッシュを足すと捨てるコードが増える。測定した性能の問題も示せない。
- 初出: B-033
- 再出: 0

## R-046 `NewValue` がセルごとにバイト列をコピーする
- 対象: internal/sqlvalue/sqlvalue.go の NewValue
- 指摘: コピーのために確保が増え、GC の負荷が上がる。
- 却下の理由: `sql.RawBytes` のバッファは次の `Scan` で上書きされるので、行を持ち越す型付きの値にはコピーが必須（`TestNewValueCopiesRaw`）。コピーを省くと値が壊れる（データの破損）。代案の「`[]byte` に Scan する」も、結局ドライバが 1 回コピーするので、確保の数は同じ。
- 初出: B-033
- 再出: 0

## R-047 `Value` のゼロ値が NULL ではなく空文字列になる
- 対象: internal/sqlvalue/sqlvalue.go の Value
- 指摘: `Value{}` は `''` にリテラル化され、設定し忘れたセルが黙って空文字列で書かれる。
- 却下の理由: `Value` のフィールドは非公開で、`NewValue` 以外では作れない（パッケージ外から `Value{}` を作れても、型名がなくセルとして使えない）。コードの中に `Value{}` を作る経路はなく、`Rows` は全セルを `NewValue` で埋める。失敗のシナリオを示せない（推測）。
- 初出: B-033
- 再出: 0

## R-048 `[][]Value` から `[][]string` への変換を `dump` とテストのヘルパーで繰り返している
- 対象: internal/dump/dump.go、internal/source/itest_test.go の rowsOf
- 指摘: 同じ変換のループが 2 か所にあり、共通の `LiteralRows` にまとめるべき。
- 却下の理由: 行単位の変換は `sqlvalue.Literals` にまとめ済み。残るのは `range` 1 つの数行で、2 か所のみ。B-034 でライタがインターフェースになると、`dump` 側の変換は消える。好みの問題。
- 初出: B-033
- 再出: 0

## R-049 `dump` のリテラル化の段にユニットテストがない
- 対象: internal/dump/dump.go の dumpDB
- 指摘: CLAUDE.md の「テストを先に書く」に反して、`dump` 側の変換のテストが単体テストにない。
- 却下の理由: 変換は `sqlvalue.Literals`（`TestLiterals`）と `Value.Literal`（`TestValueLiteral`）で単体テスト済み。`Rows` の並べ替えと出力のバイト列は、既存の統合テスト（`TestRowsNoKeyDuplicates`、`TestValueLiterals`、dump の統合テスト）が見ており、MySQL 8.0 / 8.4 で実行済み。受け入れ条件は「既存のテストが変更なしで通る」。
- 初出: B-033
- 再出: 0

## R-050 `Writer` のインターフェースが `schemafile.View` と MySQL の `SHOW CREATE` 文字列を持つ
- 対象: internal/writer/writer.go の Schema
- 指摘: 他 DBMS のライタが MySQL 向けの型・DDL 文字列に依存する。ビューやテーブル定義を構造で持つべき。
- 却下の理由: B-034 の範囲は「インターフェースを定義し、MySQL 向けをその実装にする」こと（受け入れ条件）。定義を構造化するには `source` が読み出す形を変える必要があり、PostgreSQL / SQLite の方言差を扱う B-035、B-036 の範囲。`Schema` の doc コメントで MySQL の `SHOW CREATE` 文字列であることを明記済み。失敗のシナリオ（現時点で壊れるもの）は示せない。
- 初出: B-034
- 再出: 0

## R-051 `DataParts` にエラー戻り値がない
- 対象: internal/writer/writer.go の Writer.DataParts
- 指摘: 描画できない値を持つ将来のライタが、エラーを返せず `Part` の中で失敗するしかない。
- 却下の理由: MySQL 向けの実装は失敗しない（`sqlvalue.Literals` はエラーを返さない）。失敗を返す具体的なライタは存在せず、推測。`Part` の戻り値でエラーは報告でき、`dump` はそれを書き出し失敗として扱う（一時ファイルは残らない）。必要になる B-035 で、実際の失敗の形に合わせてシグネチャを決める。
- 初出: B-034
- 再出: 0

## R-052 `dump` の `Writer` 経由の配線が単体テストにない
- 対象: internal/dump/dump_test.go の TestWritesThroughWriter
- 指摘: ライタ経由で書く配線のテストが `MYSQLDANCE_TEST_DSN` を要する統合テストで、`task test` では走らない。
- 却下の理由: `Run` は先に MySQL に接続するので、配線を通すには実機が要る。CLAUDE.md の運用どおり、`internal/dump` を変えたときは `task test:integration` を通す。この項目では MySQL 8.0 / 8.4 で実行済み。切り出せる部分（`dataParts` の 0 件の拒否、`Data` と `Writer` の併用の拒否）は単体テストにした。
- 初出: B-034
- 再出: 0

## R-053 SQLite ライタで、大文字小文字だけ違うテーブル名が衝突する
- 対象: internal/writer/sqlite.go の WriteSchema（`taken`）
- 指摘: `Users` と `users` が同じ DB にあると、`taken` を小文字で引くので後のテーブルが前のテーブルを上書きし、リストアで `table already exists` になる。
- 却下の理由: 到達しない。`dump` が出力ファイル名を小文字にして衝突を検査し（`claim`）、衝突する名前は書き出す前にエラーにする（設計書 4.1 節、`TestOddNamesAndCaseCollision`）。ライタに届くテーブル名は、小文字にして一意である。
- 初出: B-036
- 再出: 0

## R-054 SQLite ライタが AUTO_INCREMENT を警告しない
- 対象: internal/writer/sqlite.go の sqliteColumn（`ColumnDef.AutoIncrement`）
- 指摘: 単一列の `INTEGER PRIMARY KEY` でない `AUTO_INCREMENT` 列は、SQLite で自動採番されなくなるのに、警告がない。
- 却下の理由: 仕様どおり。MySQL 向けも `AUTO_INCREMENT=N` を出力から除いており（README の「AUTO_INCREMENT is not restored」）、SQLite 向けは README の「SQLite output」に「`AUTO_INCREMENT` は引き継がない」と書いた。出力されるデータの値は変わらない。単一列の整数主キーは SQLite の rowid の別名になり、採番は続く。
- 初出: B-036
- 再出: 0

## R-055 SQLite ライタの警告が、後続の失敗で捨てられるファイルについても出る
- 対象: internal/dump/dump.go の dumpDB（`Warn`）、internal/writer/sqlite.go の WriteSchema
- 指摘: スキーマの書き出し後に別の失敗でダンプ全体が巻き戻っても、そのスキーマファイルについての警告が残る。
- 却下の理由: 警告は入力（MySQL の定義）について出すもので、MySQL 向けの警告（非 InnoDB、大きなテーブル）も、書き出しの前に出して同じく巻き戻りで残る。失敗した実行の警告が残っても害はなく、実行が失敗したことはエラーで分かる。`WriteSchema` が失敗したときに出さないのは、出力できなかった定義を「出力しない」と言わないための最低限。
- 初出: B-036
- 再出: 0

## R-056 `Snapshot.Defs` が `listTables` をもう一度実行する
- 対象: internal/source/def.go の Defs
- 指摘: `Tables` と `Views` が読んだテーブル一覧を再利用せず、`--target sqlite` のとき DB ごとに `information_schema.TABLES` を余分に 1 回引く。
- 却下の理由: 効率だけの指摘で、失敗のシナリオはない。`--target sqlite` のときだけ、DB ごとに 1 クエリ。`Tables`、`Views` も同じ方式で独立に引いており（取得部をテーブル単位で独立に呼べる構造、設計書 8 節）、一覧のキャッシュを `Snapshot` に持たせると、`Use` で捨てる状態が増える（R-007、B-021 の「隠れた状態をなくす」と逆向き）。
- 初出: B-036
- 再出: 0

## R-057 BIT の規則が `sqliteType`、`sqliteDefault`、`SQLiteLiteral` に分かれている
- 対象: internal/writer/sqlite.go の sqliteType・sqliteDefault、internal/sqlvalue/sqlite.go の SQLiteLiteral
- 指摘: BIT(n) の幅の閾値（63）が列の型を決める側にだけあり、値のリテラル化は幅を知らないので、食い違うと型と値がずれる。
- 却下の理由: 食い違いは起きない。`SQLiteLiteral` は、値が int64 に収まれば素の整数、収まらなければ文字列で書く。列が `INTEGER`（幅 63 以下）なら値は必ず収まり、`TEXT`（幅 64）なら素の整数も文字列も値を変えずに入る。`TestSQLiteValuesRoundTrip` が BIT(64) の 0x7f..、0xff..、BIT(8) で確認している。型の規則を 1 つにまとめる変更は、挙動を変えずに構造だけ変える好みの問題。
- 初出: B-036
- 再出: 0

## R-058 AUTO_INCREMENT・ENUM/SET の制約・照合順序が PostgreSQL 向けの出力で警告なしに落ちる
- 対象: internal/writer/postgres.go の Postgres（pgColumnType）
- 指摘: `AUTO_INCREMENT` が素の整数になり、`ENUM`/`SET` が制約なしの `text` になり、照合順序が落ちるのに `Warn` が出ない。「黙って落とさない」に反する。
- 却下の理由: 設計書 10.5 節の【判断】（`AUTO_INCREMENT` はカウンタを復元しない）と確定した仕様どおりで、README の「PostgreSQL output」に明記している（`AUTO_INCREMENT` は素の整数、コメント・文字コード・照合順序は落とす、`ENUM`/`SET` は `text`）。値は一つも変わらず、全行が復元される。SQLite 向け（B-036）も同じ扱いで、警告は定義を表せないとき（生成列、CHECK など）に限る。毎回出る警告は雑音になる。
- 初出: B-035
- 再出: 0

## R-059 `CURRENT_TIMESTAMP(n)` の既定値が精度を落として `CURRENT_TIMESTAMP` になる
- 対象: internal/writer/postgres.go の postgresDefault
- 指摘: `DEFAULT CURRENT_TIMESTAMP(6)` が精度なしの `CURRENT_TIMESTAMP` として書かれ、警告もない。
- 却下の理由: PostgreSQL は既定値を列の精度（`timestamp(n)` / `timestamptz(n)`）に丸めて入れるので、`CURRENT_TIMESTAMP` と `CURRENT_TIMESTAMP(n)` は同じ値になる。列の精度は `pgPrecision` で MySQL の列から引き継いでいる。値の違いを示すシナリオはない。
- 初出: B-035
- 再出: 0

## R-060 接頭辞インデックスを列全体のインデックスにすると、大きな値の復元が失敗する
- 対象: internal/writer/postgres.go の writePostgresTable（`Prefix`）
- 指摘: `KEY(body(100))` を列全体のインデックスにすると、約 2.7KB を超える値で PostgreSQL の btree の行サイズ上限に当たり、復元が失敗する。警告はダンプ時にしか出ない。
- 却下の理由: 失敗は復元時にエラーとして必ず出て、握りつぶされない（不変条件は満たす）。ダンプ時にも警告を出している。PostgreSQL に接頭辞インデックスはなく、代わりに式インデックスを作ると意味が変わる。SQLite 向け（B-036）も同じ方針。
- 初出: B-035
- 再出: 0

## R-061 `TableInfo.Def` が `dump` の設定する裏口のポインタである
- 対象: internal/source/source.go の TableInfo.Def、internal/dump/dump.go の dumpDB
- 指摘: `Writer` のインターフェースに定義を渡す場所がなく、`TableInfo.Def` を `dump` が埋める形は裏口になっている。
- 却下の理由: `DataParts` に定義を渡す方法として、引数を足す案と比べて選んだ設計。ライタが `WriteSchema` で見たものを覚える形（呼び出し順への依存、B-021 で外したもの）にせず、毎回の呼び出しの入力として明示している。引数を足すと `Writer` の全実装とテスト 8 か所の署名が変わるが、得るものは同じ。`NeedsDefs` が false のライタには `nil` で、挙動は変わらない。ヘルパー（`emit`、`outputColumns`）の置き場所の指摘は受け入れて `writer.go` に移した。
- 初出: B-035
- 再出: 0

## R-062 `viewRefs` の部分文字列照合が偽の依存を作り、循環でエラーになりうる
- 対象: internal/source/dialect.go の viewRefs
- 指摘: 文字列リテラルの中の `` `db`.`b` `` などが偽の依存になり、2 つのビューが循環して並べ替えが失敗する。
- 却下の理由: 循環はエラーにならない。`schemafile.OrderViews` は循環を見つけると、残りを名前順で末尾に足す（schemafile.go の OrderViews のコメントとコード）。偽の辺が増えても順序が変わりうるだけで、ダンプは失敗しない。リテラルに別のビューの修飾名を書くビューは現実には稀で、VIEW_TABLE_USAGE がない MariaDB では他に構文解析なしの手段がない。コメントに理由を書いた。大文字小文字の指摘（同じ関数）は受け入れて直した。
- 初出: B-038
- 再出: 0

## R-063 MariaDB の `INVISIBLE` 列が EXTRA に出ず、黙って落ちる
- 対象: internal/source/dialect.go の mariadb（`columns` を共有）
- 指摘: MariaDB が EXTRA に `INVISIBLE` を出すか未確認で、出さないと `ListColumns` が偽になり列が落ちる。
- 却下の理由: 設計書 11 節（B-037）で `INVISIBLE` は MySQL と同じと実機で確認済み。さらに `TestMariaDBRoundTrip`（`hid INT INVISIBLE`）が MariaDB 11.4 で、全列（`INVISIBLE` を含む）の値の一致まで通る。
- 初出: B-038
- 再出: 0

## R-064 SEQUENCE / SYSTEM VERSIONED を含む MariaDB の DB が丸ごとダンプできない
- 対象: internal/source/source.go の checkTableType
- 指摘: 通常の MariaDB のオブジェクトなのに、1 つあるだけで DB 全体が失敗する。落とすか出力すべき。
- 却下の理由: B-038 の確定した仕様（「エラーにする。出力はしない」）。SYSTEM VERSIONED は `SELECT` が履歴行を返さず、黙って落とすと「黙って落とさない」に反する。README の MariaDB 節に明記し、`TestUnsupportedTableTypes` / `TestUnsupportedTableTypeKeepsPreviousDump` で、名前つきのエラーと、既存ファイルが変わらないことを確認済み。対応するのは別項目。
- 初出: B-038
- 再出: 0

## R-065 `'a' || 'b'` のような式の既定値が、引用符で始まり終わるため文字列として扱われる
- 対象: internal/source/dialect.go の mariadbColumnDefault
- 指摘: 先頭と末尾が `'` の値を文字列リテラルとして切り取るので、式を取り違える。
- 却下の理由: MariaDB 11.4 の実機で、式の既定値は `('a' or 'b')`（括弧つき）や `concat('a','b')`（関数）で報告され、引用符で始まり終わる式は出なかった。リテラルでない既定値は構文上、括弧か関数になる。
- 初出: B-038
- 再出: 0

## R-066 `viewDefinitions` が 1 つのビューの定義が空だと全体を失敗させる
- 対象: internal/source/dialect.go の viewDefinitions
- 指摘: SHOW VIEW 権限のないビューなどで、MySQL の経路は失敗しないのに MariaDB だけ失敗する。
- 却下の理由: 空の定義を「依存なし」と読むとビューの順序が黙って狂う（不変条件に反する）ため、エラーが正しい。`Views` は先にビューごとに `SHOW CREATE VIEW` を実行するので、権限がなければ MySQL でもそこで失敗する。挙動は同じ。
- 初出: B-038
- 再出: 0

## R-067 `SELECT VERSION()` をセッション設定より先に実行する順序のリスク
- 対象: internal/source/source.go の start
- 指摘: セッションを固定する前にクエリを実行する。順序の依存にテストがない。
- 却下の理由: 失敗のシナリオがない（`VERSION()` の結果は sql_mode や文字コードに依存しない）。方言がセッション設定を決めるので、この順序が必要。`TestSessionSettingsPinned` が、敵対的な sql_mode から始めた接続で、設定の固定を両サーバーで確認する。
- 初出: B-038
- 再出: 0

## R-068 MariaDB の `Type` の表示幅（`int(11)`）を使う書き出し側が未確認
- 対象: internal/source/def.go の ColumnDef.Type、internal/writer の sqlite.go / postgres.go
- 指摘: tinyint(1) と同じ種類の問題が、他の型に残る。
- 却下の理由: 失敗のシナリオを示せない。`Type` の幅を読むのは、`tinyint(1)`（修正済み）、`bit(n)`、`decimal(p,s)`、`char/varchar(n)`、時刻型の精度だけで、整数型は `DataType` で決まる（postgres.go の pgColumnType、sqlite.go の sqliteType）。`pgPrecision` は時刻型にしか使わない。`TestSQLiteRoundTrip` は MariaDB で通る。PostgreSQL は `task test:integration:mariadb` で確認する。
- 初出: B-038
- 再出: 0

## R-069 MySQL 5.7 で `show_create_table_verbosity` を設定しないと、グローバル設定で出力が変わる
- 対象: internal/source/dialect.go の mysql57（session）
- 指摘: 5.7.22 以降でグローバルが `ON` のサーバーは `ROW_FORMAT=DYNAMIC` が出力に入り、同じスキーマでも出力が変わる。決定性に反する。
- 却下の理由: 確定した仕様そのもの（B-040 の受け入れ条件「セッション設定から `show_create_table_verbosity` を外す」、設計書 11 節の方針「5.7 は版を見分けずに外す」）。5.7.21 以前は変数がなく、`SET` が `Unknown system variable` で失敗する。ユーザーの設定が出力に出るのは、グローバル設定が `ON` のサーバーに限る。未確認であることを README の「MySQL 5.7」節に明記している。
- 初出: B-040
- 再出: 0

## R-070 復元用サーバーのホストポート（33063）が固定
- 対象: Taskfile.yml の test:integration:one（RESTORE_PORT）
- 指摘: 同時に 2 つの統合テストを動かすと `docker run` が失敗する。
- 却下の理由: 既存の `PORT`（33061）と `PG_PORT`（33062）も固定の既定値で、`task test:integration PORT=...` と同じく変数で変えられる。同時実行を前提にした作りではない。
- 初出: B-040
- 再出: 0

## R-071 mysql57 が mariadb と同じ差し替えの型で、`columnDefault` の `dataType` 引数が 1 つの方言でしか使われない
- 対象: internal/source/dialect.go の mysql57 / columnDefault
- 指摘: 差し替えの重複と、未使用の引数。
- 却下の理由: 好みの問題で、`CLAUDE.md` の規約にない。セッション設定は `sessionNoVerbosity` で共有済み。B-039 の設計（設計書 11 節）が `columnDefault` に `DATA_TYPE` を渡す方式を決めている（`varchar` の `DEFAULT 'CURRENT_TIMESTAMP'` と区別するため）。
- 初出: B-040
- 再出: 0

## R-072 `isMySQL57` がテストのパッケージごとに実装されている
- 対象: internal/source/itest_test.go、internal/dump/mysql57_test.go
- 指摘: 判定が 3 か所（`isMariaDB` を含む）に重複し、本番の `dialectFor` とずれうる。
- 却下の理由: `isMariaDB` と同じ流儀の、パッケージごとのテスト用判定。テストが本番の判定を呼ぶと、本番が間違ったときにテストも一緒に間違う（「本番のコードは聞かない」という `isMariaDB` の方針）。
- 初出: B-040
- 再出: 0

## R-073 特権テストのユーザー名がデータベース名の接尾辞から作られ、長さの上限を超えうる
- 対象: internal/source/parallel_snapshot_test.go の TestParallelSnapshotPrivileges
- 指摘: `mds_` / `mdr_` に DB 名の接尾辞を付けたユーザー名が、32 文字（5.7 と MariaDB は 16 文字）を超えて `CREATE USER` が失敗しうる。
- 却下の理由: 接尾辞は `testDB` が作る 12 桁の 16 進数（`mysqldance_test_` + 6 バイト）で固定長。ユーザー名は 16 文字で、どの版の上限にも収まる。既存の `TestLockNeedsReloadPrivilege`（`mdance_` + 接尾辞）と同じ作り方で、8.0 と 8.4 で通った。
- 初出: B-041
- 再出: 0

## R-074 B-041 で確かめていない版・環境（RELOAD を付けられない環境など）への対応が B-042 の受け入れ条件にない
- 対象: BACKLOG.md の B-042、設計書 6.1
- 指摘: `RELOAD` が無い環境でのフォールバックや明示的なエラーが B-042 の条件にない。「黙って落とさない」に反する。
- 却下の理由: 失敗のシナリオがない。`RELOAD` が無いときは、今の `source.Open` が RELOAD と `--no-lock` を挙げたエラーで失敗する（`TestLockNeedsReloadPrivilege`）。並列取得も同じ `FLUSH TABLES WITH READ LOCK` を使うので、同じ経路を通る。確かめていない項目は設計書 6.1 に列挙した。B-041 の範囲は方式の検証で、B-042 の条件を変えるのは範囲外。
- 初出: B-041
- 再出: 0

## R-075 `--jobs` がテーブル数より大きいと、使わない接続を開き、無駄にロックを取る
- 対象: internal/source/source.go の startParallel
- 指摘: 2 テーブルの DB に `-j 64` を指定すると 65 接続を開き、RELOAD とロックが要る。
- 却下の理由: 利用者が明示した値で、動作は仕様どおり（失敗も不整合も起きない）。`Open` はテーブル数を `Tables` より前に知らないので、丸めるには接続の遅延オープンが要り、B-042 の範囲を超える。README の「Parallel reads」に接続数（N + 1）を書いている。
- 初出: B-042
- 再出: 0

## R-076 `--jobs` を上げるとメモリ使用量が N 倍になる
- 対象: internal/dump/dump.go の dumpDB / writeTable
- 指摘: N テーブルの全行が同時にメモリに載り、`-j 1` で動いた巨大テーブルで OOM になりうる。
- 却下の理由: 確定した仕様に「N 本のテーブルの全行が同時にメモリに載る」と明記済みで、README の「Parallel reads」と Limitations に書いた。解消は行のストリーム化（B-044、B-045）の範囲。既定は 1。
- 初出: B-042
- 再出: 0

## R-077 複数ファイルに分割した名前の衝突検査が、全テーブルの書き出し後になる
- 対象: internal/dump/dump.go の dumpDB（分割後の `claim`）
- 指摘: 衝突の検出が遅くなり、無駄な読み出しが増える。
- 却下の理由: 検出が遅れるだけで、エラーは commit の前に必ず返り、出力先は変わらない（R-029 と同じ）。claim をテーブル順に行うのは、並列でも返るエラーを決定的にするため（確定した仕様）。
- 初出: B-042
- 再出: 0

## R-078 `firstError` が `context.Canceled` を包んだ本物のエラーを、キャンセルの副作用として読み飛ばす
- 対象: internal/dump/dump.go の firstError
- 指摘: ドライバが `context.Canceled` を包む本物のエラーを返すと、テーブル順の保証が崩れる。
- 却下の理由: 推測。親の ctx が生きているのに `Canceled` で失敗するのは、eachTable が自分でキャンセルした場合だけで、その前に本物のエラーが必ず 1 つある。全てが Canceled 型のときは先頭を返す。
- 初出: B-042
- 再出: 0

## R-079 `Close` が全読み出し接続に順に ROLLBACK し、タイムアウトを共有する
- 対象: internal/source/source.go の Close
- 指摘: キャンセル後に死んだ接続の ROLLBACK のエラーが並び、停止したサーバーでは先頭の ROLLBACK が時間を使い切る。
- 却下の理由: 1 接続のときと同じ設計（R-022、R-025）。ROLLBACK に失敗しても、その後で全接続を閉じるのでセッションとトランザクションは終わる。失敗は報告されるべきエラーで、握りつぶさない。統合テスト（TestJobsCloseEndsEverySession）で全セッションが閉じることを確認した。
- 初出: B-042
- 再出: 0

## R-080 `Jobs` と `NoLock` の検査が `Open` と `main` で重複している
- 対象: internal/source/source.go の Open、cmd/mysqldance/main.go
- 指摘: 同じ検査が 2 か所にあり、ずれうる。
- 却下の理由: 好みの問題。`main` は使い方のエラー（フラグ名つき）、`Open` はライブラリとして直接呼ばれたときの防御で、メッセージの宛先が違う。両方をテスト済み（TestRunUsageErrors、TestJobsRejectedBeforeConnecting）。
- 初出: B-042
- 再出: 0
