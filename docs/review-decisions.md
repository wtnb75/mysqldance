# Review decisions

`impl-backlog` のコードレビューで**却下した指摘**の台帳。同じ指摘が再び出たとき、再検討せずにこの台帳で判断するために残す。受け入れた指摘（コードを直したもの）は書かない。

## 書き方

```markdown
## R-NNN 指摘の要旨（1行）
- 対象: path/to/file.go の関数名やパターン
- 指摘: レビューが何を問題としたか
- 却下の理由: なぜ問題ではないか。再現テストや仕様（設計書の節、確定した仕様）への参照を含める
- 初出: B-NNN（項目の ID だけを書く。PR の番号は書かない。PR を作るまで決まらず、番号だけの commit が要るため）
- 再出: 回数（同じ指摘が再び出るたびに増やす）
```

- ID は `R-NNN`（3桁以上、最大 ID + 1）。
- 対象のコードが変わって理由が当てはまらなくなったら、項目を見直して更新または削除する。
- 再出が 1 以上の項目は、指摘された箇所に理由のコメント（英語）が足りていない。コメントを足す。
- B-049 で `internal/source/source.go` を分けた。「対象」に `source.go` とある項目の関数は、次のファイルにある。接続・ロック・スナップショット（`Open`、`start`、`startParallel`、`lockGlobal`、`pingWithin`、`Use`、`use`、`checkDatabases`、`firstNonInnoDB`、`Close`）は `connect.go`、カタログ（`Tables`、`checkTableType`、`catalogTypeName(s)`、`tableColumns`）は `catalog.go`、行の読み出し（`Rows`、`EachRow`）は `rows.go`、型（`TableInfo`、`Snapshot`）は `source.go`。対象の書き換えはしない。

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
- 再出: 1（B-056 で全 DB の確認を `Open` に移したとき、接頭辞が付かない点として再出。dump.go の Open のエラー処理にコメントを足した）

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
- 再出: 1（B-043: internal/dump/jobs_determinism_test.go の jobsFixture。理由のコメントを足した）

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
- 再出: 2（B-044: `EachRow` のキーなしの経路。`source.go` のコメントに R-045 を書いてある。B-045: 外部ソートにした後も、キー作成と行の直列化で 2 回リテラル化・割り当てが走るという指摘。`rowcodec.go` の `appendSortKey` にコメントを足した）

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
- 再出: 1（B-095。5.6 用に `isMySQL56` / `serverVersion` を足したときに再び出た。両パッケージの `serverVersion` に理由のコメントを足した）

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
- 再出: 1（B-044: ストリーム化で無駄になる書き込みが増える指摘。`dumpDB` の `claim` にコメントを足した）

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

## R-081 `TestJobsTablesAndWarningsSameAsSerial` は `Tables` が `Jobs` を読まないので失敗しえない
- 対象: internal/source/jobs_test.go の TestJobsTablesAndWarningsSameAsSerial
- 指摘: `Snapshot.Tables` は先頭の接続だけを使い `Jobs` を参照しないので、並列度を変えても比べるのは同じ呼び出しで、何も確かめていない。
- 却下の理由: 確定した仕様（B-043）は「テーブル一覧と警告は並列度によらず先頭の接続で直列に出る」ことの固定で、今の実装がそうであることが前提。メタデータの読み出しを並列側に移す変更が入ったときに、順序の退行を捕まえる回帰テストとして置いている。失敗するシナリオを今の実装で作れないのは、テストが守る性質が成り立っているため。
- 初出: B-043
- 再出: 0

## R-082 並列の読み出し中に出る警告の順序が検証されていない
- 対象: internal/dump/jobs_determinism_test.go の TestJobsOutputAndLogIndependentOfJobs
- 指摘: MySQL ライタのログは空どうしの比較で、並列の読み出し中の警告の順序は確かめていない。
- 却下の理由: 並列の読み出し・描画（`writeTable`）は警告を出さない。警告の出どころは `source.Tables`（先頭の接続）と `Schema.Warn`（`WriteSchema` の中）で、どちらも直列。存在しない経路のテストは書けない。`writeTable` が警告を出すようになる項目で、その順序のテストも一緒に足す。
- 初出: B-043
- 再出: 0

## R-083 グローバルの `largeTableRows` の書き換えと、閉じないスナップショットの接続数
- 対象: internal/source/jobs_test.go の TestJobsTablesAndWarningsSameAsSerial
- 指摘: `t.Parallel` が入ると競合する。1 テストで最大 1 + 2 + 8 接続を開き、`max_connections` を使い切りうる。
- 却下の理由: 前者は TestWarningsLargeTable と同じ方式で、このパッケージに `t.Parallel` はない（`t.Parallel` を使わない理由は、このファイルの「競合する読み手がいない」の項と同じ）。後者は `openSnapshot` が `t.Cleanup` で閉じ、使う接続は最大 11 本で、既定の `max_connections`（151）に対して十分小さい。
- 初出: B-043
- 再出: 0

## R-084 キーのあるテーブルの書き出しが途中で失敗すると、`rows.Close()` が残りの行を読み捨てる
- 対象: internal/source/source.go の EachRow
- 指摘: 書き込みの失敗で戻っても、ドライバが結果の残りを読み切るので、中止が全表の読み出し時間だけ遅れる。
- 却下の理由: 遅れは 1 テーブルの全走査までで、出力・決定性・データには影響しない（失敗時は commit されず、一時ファイルは消える）。コンテキストを取り消して接続を壊す案は、同じ接続の読み出しスナップショットも壊すため、`EachRow` の後に同じ `Snapshot` を使える性質（`TestEachRowStopsOnCallbackError`）を失う。100 万行の読み出しは数秒で、測定した問題も示せない。
- 初出: B-044
- 再出: 0

## R-085 行ごとにハッシュを 2 回計算し、文字列を 2 回作る
- 対象: internal/datafile/stream.go の Stream.Add / fileStream.add
- 指摘: ファイルの区切り（`keyHash`）と INSERT の区切りで、同じ行を別々にハッシュする。
- 却下の理由: 旧実装（`Split` と `WriteStatements`）と同じ計算量で、増えていない。割り当ての削減は B-017 の範囲で、出力は変わらない。
- 初出: B-044
- 再出: 0

## R-086 `Snapshot.Rows` と `datafile.Write` / `WriteStatements` / `Split` が、本番のコードから呼ばれずテスト専用になった
- 対象: internal/source/source.go の Rows、internal/datafile/datafile.go
- 指摘: 全行をメモリに載せる公開メソッドや、本番で使わない関数が残っている。`_test.go` に移すべき。
- 却下の理由: 好みの問題で、失敗のシナリオがない。`Rows` は `EachRow` を集めるだけの薄い関数で、統合テストの比較に使う。`Write` / `WriteStatements` は `Stream` の 1 ファイル用の入口で、`Split` は旧実装との差分テストで境界の規則を独立に確かめる基準（コピーを持つと規則がずれたときに検出できない）。
- 初出: B-044
- 再出: 0

## R-087 トリガーの `character_set_client` / `collation_connection` を残さない
- 対象: internal/schemafile/triggers.go の WriteTriggers
- 指摘: 作成時の接続の文字コードを残さないと、復元したトリガーの属性が変わり、照合順序に敏感な比較の挙動が変わりうる。
- 却下の理由: B-046 で確定した仕様（設計書 12 節の B-046、README の Triggers 節）。全ファイルは固定ヘッダーの `SET NAMES utf8mb4` で流し、本体のテキストは `utf8mb4` で読み書きするので、本体は変わらない。属性まで残すと、サーバーや接続の設定でダンプが変わり、「環境依存の値を出力に混ぜない」（決定性）に反する。`sql_mode` は本体の解釈を変えるので残す。
- 初出: B-046
- 再出: 1（B-048: イベントの `character_set_client` / `collation_connection` / `Database Collation` でも同じ指摘。理由は同じで、`schemafile.WriteEvents` にコメントを足した）

## R-088 ダンプしたテーブルに属さないトリガーも出力される
- 対象: internal/dump/dump.go の dumpDB（snap.Triggers）
- 指摘: `snap.Tables` が飛ばしたテーブルのトリガーが出て、復元で `CREATE TRIGGER` がテーブル欠落で失敗する。
- 却下の理由: `source.Tables` は BASE TABLE を 1 つも飛ばさない（警告は出すが全て返す。source.go の Tables）。トリガーは BASE TABLE にしか付かないので、全トリガーの表はダンプされるテーブルに含まれる。
- 初出: B-046
- 再出: 0

## R-089 SHOW CREATE TRIGGER を `snap.Readers()` で並列化する
- 対象: internal/source/triggers.go の Triggers
- 指摘: トリガーが数千あると 1 件ずつの往復で遅い。
- 却下の理由: 失敗のシナリオがない速度の好み。ビューの `SHOW CREATE VIEW` も同じ直列で、トリガーが数千ある DB は想定の規模（MVP は小さな DB）の外。並列化は別の項目で測ってから決める。
- 初出: B-046
- 再出: 0

## R-090 トリガーに `StripDefiner`（ビュー向けの正規表現）を使っている
- 対象: internal/writer/mysql.go の WriteTriggers
- 指摘: 特殊な DEFINER の書き方だと正規表現に一致せず、DEFINER が残る。
- 却下の理由: `SHOW CREATE TRIGGER` の DEFINER は `SHOW CREATE VIEW` と同じく常にバッククォートで引用された `` `u`@`h` `` で出る。正規表現の `ident` はこの形を扱い、`TestStripDefinerQuotedUsers` が特殊な文字を含むユーザー名を確かめている。トリガーの出力でも `DEFINER` が残らないことは統合テストで確認した。
- 初出: B-046
- 再出: 1（B-048: イベントの `StripDefiner` でも同じ指摘。`SHOW CREATE EVENT` も同じ引用形で、`TestStripDefinerEvent` と統合テスト `TestEventsRoundTrip` が `DEFINER` が残らないことを確かめている）

## R-091 テストの `clientStatements` が mysql クライアントの近似でしかない
- 対象: internal/dump/dump_test.go の clientStatements
- 指摘: 文字列やコメントの中の区切り文字など、本物のクライアントと違う扱いがあり、テストが通っても実際の復元が失敗しうる。
- 却下の理由: ヘルパーはダンプが出す形（`DELIMITER` の行と、文の末尾の区切り）だけを扱う。文字列やコメントの中の区切り文字は、`chooseDelimiter` が文の中に現れない区切りを選ぶので起きない（`TestWriteTriggersDelimiterAvoidsBody`）。本物の `mysql` クライアントでの復元は、検証（完了前）で手で確かめる。
- 初出: B-046
- 再出: 0

## R-092 `Open` を出力先の準備より前に呼ぶと、ローカルの失敗までロックを持つ
- 対象: internal/dump/dump.go の Run（Open の後の prepareOutDir / sweepTemps）
- 指摘: 非 InnoDB のテーブルや `--jobs` 2 以上では `Open` がグローバルな読み取りロックを取る。その後 `prepareOutDir` や `sweepTemps` が失敗すると、その間ロックを持ったままになる。
- 却下の理由: 受け入れ条件（B-056）が「不備のある DB の指定では出力先を作成・削除しない」ことなので、`Open` を先にするのは仕様の帰結。`prepareOutDir` と `sweepTemps` はローカルの短い処理で、失敗しても `defer` の `snap.Close` がロックを解く。データの破損や欠落はない。好みの問題。
- 初出: B-056
- 再出: 0

## R-093 全 DB の存在確認と `Use` で、同じ問い合わせを 2 回行う
- 対象: internal/source/source.go の checkDatabases / use
- 指摘: 各 DB を `checkDatabases` と `Use` で 2 回確かめる。確認の場所が複数ある。
- 却下の理由: 前者はロックの前に不備を見つけるため、後者は `USE` の直前にそのスナップショットで確かめるため（確認から読み出しの間に DB が消えるのを検知する）。DB 数ぶんの軽い 1 行の問い合わせで、実測しても無視できる。システム DB の確認は `checkDatabase` に集約済み。
- 初出: B-056
- 再出: 0

## R-094 確認とロックの間に DB が削除されると、出力先を触った後で失敗する
- 対象: internal/source/source.go の start / startParallel（checkDatabases の後）
- 指摘: 確認の後、ロックの前後で DB が削除されると、後の `Use` で初めて分かり、そのときには出力先が作られている。
- 却下の理由: その場合も失敗は最終パスの変更より前で、`Run` は一時ファイルを消して終わる（README の「Failure handling」）。ダンプ中の DDL に対する競合は、README が「DDL を同時に走らせない」前提にしている範囲。再現テストは書けない（時間窓）。
- 初出: B-056
- 再出: 0

## R-095 `TestSameDatabasesTwiceSameChecksum` が変更と関係ない
- 対象: internal/dump/dump_test.go の TestSameDatabasesTwiceSameChecksum
- 指摘: 既存の決定性のテストと重複し、実行時間が増える。
- 却下の理由: 受け入れ条件（B-056）の 3 つ目が「同じ指定で 2 回作ったデータが一致する（行のチェックサムで確認する）」を求めている。複数 DB の指定（今回の変更が触る経路）で全ファイルのチェックサムを比べるテストは、他に無い。
- 初出: B-056
- 再出: 0

## R-096 `lockGlobal` の後始末の `SET SESSION lock_wait_timeout = DEFAULT` が元の値に戻さず、失敗も見ない
- 対象: internal/source/source.go の lockGlobal
- 指摘: セッションに元から別の値があると、サーバーの全体値に置き換わる。リセットの失敗を無視し、取り消された ctx を使う。
- 却下の理由: この接続のセッション値は、ツール自身が組み立てた接続（DSN に `lock_wait_timeout` を入れない）で、`lockGlobal` でしか設定しない。元の値は常にサーバーの値なので `DEFAULT` が元の値と同じ。リセットに失敗する場面（接続が壊れた、ctx が取り消された）では、以降の読み出しも失敗するので、黙って壊れたデータにはならない。コードにその理由のコメントを足した。
- 初出: B-057
- 再出: 0

## R-097 既定の `--lock-timeout 30s` は、長い文を待っていた従来の実行を失敗させる
- 対象: cmd/mysqldance/main.go の `--lock-timeout` の既定値
- 指摘: 既定を 30 秒にすると、従来は待って成功した実行が、既定で失敗する（互換性）。0 を既定にすべき。
- 却下の理由: 既定値と、超えたらエラーで終了することは、B-057 の仕様確定でユーザーが選んだ（`30s`、`0` は上限なし）。無限に近い待ちで他の文を巻き込み続けるのが、この項目で直す問題。README のフラグ表と「Non-InnoDB tables and the read lock」に、上限と回避方法（延ばす、`0`、`--no-lock`）を書いた。
- 初出: B-057
- 再出: 0

## R-098 FTWRL を取らない実行で `--lock-timeout` が黙って無視される
- 対象: cmd/mysqldance/main.go の `--lock-timeout`
- 指摘: `--no-lock` や InnoDB だけの `--jobs 1` ではロックを取らず、フラグが何もせず警告も出ない。
- 却下の理由: 上限は「ロックを取るなら、待ちはこれまで」という条件で、取らないなら守るべき待ちが無い。読めない列や衝突するファイル名のように、データを落とすわけではない（CLAUDE.md の「黙って落とさない」の対象外）。既定で常に 30s が入るので、警告を出すと InnoDB だけの普通の実行に毎回警告が出てしまう。
- 初出: B-057
- 再出: 0

## R-099 `TestLockTimeout` が実時間に依存し、遅く、不安定になりうる
- 対象: internal/source/lock_test.go の TestLockTimeout
- 指摘: 6 秒の `SLEEP` と 4 秒の上限で CI が不安定になる。ゴルーチンの `t.Errorf` がテスト終了後に走りうる。
- 却下の理由: 受け入れ条件が「長いクエリが動いているサーバーで、上限内に失敗する」ことを統合テストで確かめるよう求めている。実機の待ちを使わずには確かめられない。`t.Cleanup` が `longDone` を待つので、`t.Errorf` はテスト終了後には走らない。
- 見直し（B-094）: 「4 秒の余裕があり通った」は誤りだった。CI で間欠的に失敗し（B-094）、実機でも 8.4 で、サーバーのセッション削除が非同期なのに直後に数える競合を確認した。実時間の待ちは要るが、短い固定の `SLEEP` ではなく、長い `SLEEP`（60 秒）を `KILL QUERY` で止める形にし、`User sleep` の状態を待ってから `Open` し、残りセッションは期限つきでポーリングする。指摘のうち「固定の短い待ちで不安定になる」は今は当たらない。
- 初出: B-057
- 再出: 0

## R-100 `lockGlobal` の呼び出し側に後始末が分かれている
- 対象: internal/source/source.go の lockGlobal、start、startParallel
- 指摘: `why` の組み立て、接続を閉じる処理、エラーの包み方が 3 か所に分かれ、将来の変更で 3 か所に触れる。
- 却下の理由: 推測で、失敗のシナリオが無い。ロックの取り方は `lockGlobal` に集約済みで、呼び出し側が持つのは接続ごとに違う後始末（単独接続は閉じる、並列は defer でまとめて閉じる）と、`why` の文面（`--no-lock` と `--jobs 1` の違い）だけ。
- 初出: B-057
- 再出: 0

## R-101 `TestMySQL57ZeroDateDefault` のプローブ用 DB の `USE` が、2 つ目のサーバーの管理接続の既定 DB を動かす
- 対象: internal/dump/mysql57_test.go の TestMySQL57ZeroDateDefault（`second.createDB(t, db+"_probe")`）
- 指摘: プローブ後、管理接続の既定 DB がプローブ用 DB になり、後で修飾名なしの文を足すと別の DB に当たる。余分な DB と DROP が増える。
- 却下の理由: 失敗のシナリオが無い（今の `contents` は `db.table` と修飾し、`restore` は `DBName=""` の別接続）。好みの問題。テストに理由を 1 行のコメントで残した。
- 初出: B-066
- 再出: 0

## R-103 `EachRow` の FLOAT の変換で、範囲変数 `b` と関数全体の `err` を再代入している
- 対象: internal/source/source.go の EachRow（`b, err = sqlvalue.ExactFloat(b)`）
- 指摘: 外側の `err` を上書きし、シャドーイングと読み違えやすい。
- 却下の理由: 失敗のシナリオが無い（推測）。`err` は直後に `return` で使うだけで、ループの後は `rows.Err()` を別に呼ぶ。好みの問題。
- 初出: B-059
- 再出: 0

## R-104 `IsFloat` が `Literal` と型名の正規化を二重に持つ
- 対象: internal/sqlvalue/float.go の IsFloat、sqlvalue.go の Literal
- 指摘: 大文字化・trim・`UNSIGNED ` の除去が 2 か所にあり、型の綴りを足すとずれる。
- 却下の理由: 今のずれは無い（推測）。`DatabaseTypeName` が返す綴りは `FLOAT` と `UNSIGNED FLOAT` だけで、どちらも両方が扱う（`TestIsFloat`、`TestLiteral`）。好みの問題。
- 初出: B-059
- 再出: 0

## R-105 `FLOAT` の最大値に対する正確な `DOUBLE` 表記が、サーバーに受け入れられる確証がない
- 対象: internal/sqlvalue/float.go の ExactFloat（範囲外のときの退避）
- 指摘: 17 桁の表記をサーバーが拒否・丸めると、復元が失敗するか値が変わる。
- 却下の理由: 実機で確かめ済み。`TestFloatDoubleRoundTrip` が `MaxFloat32` と `-MaxFloat32` を含む 300 行を、8.0 と 8.4 でビット単位の一致まで通る。`3.4028234663852886e38` は受け入れられ、`3.4028235e38` は拒否されることも 8.4 で確認した（設計書 11 節）。
- 初出: B-059
- 再出: 0

## R-102 `BACKLOG.md` の `# Done` で新しい項目を先頭に置くと、並びの規則が崩れる
- 対象: BACKLOG.md の `# Done` 節（B-066 の位置）
- 指摘: Done 節の並びが完了順か ID 順か不明で、先頭への追加が規則を壊しうる。
- 却下の理由: Done 節は ID 順でも完了順でもなく（B-057 の後に B-056、B-041、B-039 …と続く）、直近の完了を先頭に足す運用になっている。`scripts/backlog.awk` は並びに依存しない。
- 初出: B-066
- 再出: 0

## R-103 `sqlite3` コマンドのテストが、`sqlite3` の有無を確かめる前に MySQL のダンプを作る
- 対象: internal/dump/sqlite_cli_test.go の restoreSQLiteCLI（`exec.LookPath`）
- 指摘: `sqlite3` が無い環境では、DB の作成とダンプをしてから skip するので、skip が遅い。
- 却下の理由: 効率だけの指摘で、失敗のシナリオはない。統合テストは MySQL が有るときだけ動き、無駄になるのは数十ミリ秒。`sqlite3` が無い環境は skip して、CI では失敗にする（B-069 で確定）。
- 初出: B-069
- 再出: 0

## R-104 `compareSQLiteRows` の引数が、名前のない interface になっている
- 対象: internal/dump/sqlite_test.go の compareSQLiteRows
- 指摘: `*sql.DB` か、`cells` と共通の名前付き interface にすべき。
- 却下の理由: 好みの問題。`cells` が同じ形の interface を受けており、それに揃えた。`CLAUDE.md` や linter の規約にない。
- 初出: B-069
- 再出: 0

## R-105 CI の skip 検査の文言が、テストの定数と別に書かれている
- 対象: .github/workflows/integration.yml の Fail if the integration tests were skipped、internal/dump/sqlite_cli_test.go の sqliteCLISkip
- 指摘: 文言を変える、または `-v` を外すと、grep が合わなくなり、skip しても CI が通る。`sqlite3` が無い runner では、入れる手順がない。
- 却下の理由: `MYSQLDANCE_TEST_DSN` の検査（同じ job の既存の仕組み）と同じ方式で、`-v` は同じ job が既に使っている。定数のコメントに CI が grep することを書いた。`ubuntu-latest` に `sqlite3` が無ければ、job が失敗するのが意図した動作。
- 初出: B-069
- 再出: 0

## R-106 `sqlite3` の標準エラー出力が空でないだけで、テストが失敗になる
- 対象: internal/dump/sqlite_cli_test.go の restoreSQLiteCLI（`stderr.Len() > 0`）
- 指摘: 配布元の違う `sqlite3` が正常でも標準エラーに出力すると、テストが誤って失敗する。
- 却下の理由: 推測で、出力する版を示せない。`sqlite3` は失敗した文の後も続けて終了コードが 0 になりうるので、標準エラーを見ないと復元の失敗を握りつぶす（「黙って落とさない」）。実機で 3.46.1 と 3.8.2 は、成功時に何も出さない。
- 初出: B-069
- 再出: 0

## R-107 設計書の【確認済み】と、`BACKLOG.md` の CI の受け入れ条件が食い違う
- 対象: docs/superpowers/specs/2026-11-09-mysqldance-design.md 11 節（B-069）、BACKLOG.md の B-069
- 指摘: CI の結果が無いのに【確認済み】と書いてあり、項目が `todo` のまま。
- 却下の理由: 指摘の時点で、設計書は「CI の `sqlite3` では未実行」と明記し、確認済みは手元の版と Docker の版に限っていた。CI の条件が未達の間は `todo` のまま残し、PR を draft にするのが `impl-backlog` の規則。その後 PR の CI（`sqlite3` 3.46.1）が通ったので、設計書と項目を更新して `done` にした。
- 初出: B-069
- 再出: 0

## R-108 `main.go` の最終エラーが `opt.Log` の nil を考慮していない
- 対象: cmd/mysqldance/main.go の `run`（`opt.Log.Error`）
- 指摘: `dump.Run` は `opt.Log` が nil でも動くのに、呼び出し側が nil のときに panic しうる。
- 却下の理由: `parseArgsTo` は成功時に必ず `Log` を設定する（`TestLogDefaults` 系の `parseLog` が nil でないことを検証している）。`run` の中で `opt` を作り直す経路はなく、失敗のシナリオを示せない。
- 初出: B-087
- 再出: 0

## R-109 最終エラーの書式が `mysqldance: <err>` から構造化レコードに変わり、「メッセージ本文は変えず」に反する
- 対象: cmd/mysqldance/main.go の `run`、BACKLOG.md の B-087
- 指摘: 失敗の出力が平文の 1 行ではなくなり、プレフィックスで grep するスクリプトが壊れる。
- 却下の理由: B-087 の確定した仕様が「`dump.Run` の失敗が `Error` で出る（text / json）」であり、エラーの文面は `error` 属性にそのまま入る。「メッセージ本文」はエラーの文面を指す。変更は README の `--log-format` の行に書いた。
- 初出: B-087
- 再出: 0

## R-110 `failingRun` の `append(extra, ...)` が呼び出し側のスライスと領域を共有する
- 対象: cmd/mysqldance/logflags_test.go の `failingRun`
- 指摘: `extra` に余りの容量があると、呼び出し側の配列を書き換える。
- 却下の理由: `extra` は可変長引数で、呼び出しはすべてリテラルの列。余りの容量を持つスライスを渡す呼び出しはなく、失敗のシナリオを示せない。
- 初出: B-087
- 再出: 0

## R-111 閉じたポート 1 への接続を使うテストが、環境によっては失敗または待つ
- 対象: cmd/mysqldance/logflags_test.go の `failingRun`
- 指摘: プロキシや 127.0.0.1:1 で待ち受けるものがある環境では、テストが不安定になる。
- 却下の理由: サーバーを使わずに `dump.Run` を失敗させる方法として、ループバックの閉じたポートへの接続拒否は即座に返る（このテストは 0.2 秒で終わる）。実際に不安定になる環境を示せない。
- 初出: B-087
- 再出: 0

## R-112 「global read lock released」の保持時間に、接続を閉じる時間が含まれる
- 対象: internal/source/source.go の Close（`lockedAt` から `time.Since`）
- 指摘: ロックを持つ接続を閉じた後に記録するので、`duration` が閉じる時間の分だけ長い。閉じるのが失敗しても「released」と出る。
- 却下の理由: ロックは接続を閉じたときに解除される（サーバー側の動作）ので、閉じた後が解除の時点に最も近い。閉じる時間は ROLLBACK の上限（`closeTimeout`）で抑えられ、ログの値は参考情報で出力ファイルに影響しない。接続の終了が失敗しても、セッションが切れればロックは解除される。誤った値で壊れるものを示せない。
- 初出: B-088
- 再出: 0

## R-113 ロックを取った後の失敗経路で、「released」が出ない
- 対象: internal/source/source.go の start / startParallel（`lockGlobal` の後のエラー経路）
- 指摘: `acquired` の後で `START TRANSACTION` などが失敗すると、対になる `released` が出ない。
- 却下の理由: 失敗はエラーとして必ず返り（黙って落とさない）、`Open` が `sdb.Close()` で全接続を閉じてロックも解除される（R-020）。ロックの直後にサーバー側の障害が起きた場合に限る診断上の不足で、失敗するテストを書けない（その状況を作る手段がない）。B-088 の受け入れ条件は正常系の取得待ちと保持の時間。
- 初出: B-088
- 再出: 0

## R-114 「released」のメッセージと「executing」のログが複数の箇所に書かれている
- 対象: internal/source/source.go の startParallel / Close / lockGlobal
- 指摘: 同じメッセージと Debug の行が呼び出し側ごとにあり、共通のヘルパーにまとめるべき。
- 却下の理由: 好みの問題で、`CLAUDE.md` の規約にない。`released` は 2 か所（直後に解除する場合と Close で解除する場合）で、時点が違う。`simplify` で同じ指摘を検討し、箇所が 3 つだけで得るものが小さいので見送った。
- 初出: B-088
- 再出: 0

## R-115 並列度の独立性のテストが WARN の行しか比べず、Info の行を見ない
- 対象: internal/dump/jobs_determinism_test.go の `warnLines`
- 指摘: Info の行の決定性が確かめられなくなる。`level=WARN` の文字列一致は、ハンドラーの形式に依存する。
- 却下の理由: Info の行（`jobs`、`connections`、`duration`）は並列度や時間で変わるのが仕様で、比べる対象ではない。このテストの目的は出力ファイルと警告の順序（決定性）で、警告は引き続き比べている。ログの形式はテストが自分で作るテキストのハンドラーで固定している。
- 初出: B-088
- 再出: 0

## R-116 `TestLockTimeout` の残りセッションのポーリングが `waitFor` と重複している
- 対象: internal/source/lock_test.go の TestLockTimeout（`PROCESSLIST` を数えるループ）
- 指摘: 期限つきのポーリングが `waitFor` と別に手書きされていて、2 つの待ち方がある。
- 却下の理由: 好みの問題で、`CLAUDE.md` の規約にない。`waitFor` は「カウントが正になるまで」で、ここは「0 になるまで」と向きが逆。共通化には述語を取るヘルパーが要り、1 か所のために過剰。
- 初出: B-094
- 再出: 0

## R-117 `KILL QUERY` が効かないと、後始末が 60 秒待つ
- 対象: internal/source/lock_test.go の TestLockTimeout（`t.Cleanup`）
- 指摘: `KILL` が効かない（権限がない、接続の状態など）と、`<-longDone` が 60 秒ブロックし、ロックが残る。
- 却下の理由: 推測。テストは管理用アカウント（`MYSQLDANCE_TEST_DSN`、`root`）で、自分で開いた接続を `KILL QUERY` するので、権限は足りる。失敗したときは `t.Errorf` で報告し、待ちは 60 秒で終わる（無期限ではない）。
- 初出: B-094
- 再出: 0

## R-118 `test:integration:mysql56` の 2 回目が `TESTFLAGS` を `-run MySQL56` で固定している
- 対象: Taskfile.yml の test:integration:mysql56（MariaDB への 2 回目）
- 指摘: 利用者の `TESTFLAGS=-v` が 2 回目に効かない。名前が `MySQL56` を含まない 5.6 の往復テストは MariaDB に流れず、README の「Tested」の裏が取れなくなる。
- 却下の理由: 確定した仕様（B-095「1 回目は全テストを 8.4 へ、2 回目は 5.6 の往復テストだけを MariaDB へ」）そのもの。`test:integration:postgres` も `TESTFLAGS` を既定の `-run Postgres` で固定していて、同じ作り。5.6 の往復テストの名前は `TestMySQL56*` に揃えてある（Taskfile のコメントに書いた）。`-v` が要るときは 1 回目に効く。
- 初出: B-095
- 再出: 0

## R-119 README の「5.6.51 は証明書を設定すれば TLS 1.2 でつながる」に、テストの裏がない
- 対象: README.md の「MySQL 5.6」節（TLS）
- 指摘: テストにも設計書にも裏がないまま、事実として書いている。
- 却下の理由: 設計書 11 節（B-079）に実機の記録がある（自分で作った CA とサーバー証明書で起動した 5.6.51 に、`--ssl-mode required` と `verify-identity` の両方でハンドシェイクと認証が通った。`openssl s_client` でも TLS 1.2 を確認）。README の文言は「5.6.51 は」と版を限り、「前の版は TLS 1.2 を持たないことがある」と書いている。互換性の表の「Tested」にはしていない。
- 初出: B-095
- 再出: 0

## R-120 `dialectFor` が 5.6.0 から 5.6.3 を、5.6.51 以外として弾かない
- 対象: internal/source/dialect.go の dialectFor
- 指摘: 小数秒（5.6.4 から）や TLS 1.2 を持たない古い 5.6 を黙って受け付け、後で SQL やハンドシェイクのエラーになる。
- 却下の理由: 確定した仕様（B-095「拒否を 5.6 まで緩める」。設計書 11 節の方針 (1)）。5.7 も 5.7.44 以外を版で弾いていない（B-040）。確認した版が 5.6.51 だけであることは README に明記している。エラーは握りつぶされず、そのまま報告される（黙って落とさない）。
- 初出: B-095
- 再出: 0

## R-121 `TestColumnPrivilegesRefused` の後始末が `DROP USER IF EXISTS` から `DROP USER` になった
- 対象: internal/dump/dump_test.go の TestColumnPrivilegesRefused（`t.Cleanup`）
- 指摘: アカウントが既に無いと、後始末がエラーを報告する。
- 却下の理由: 推測。後始末は `CREATE USER` が成功した後にだけ登録され、途中でアカウントを消すコードはない。`DROP USER IF EXISTS` は 5.6 にない（5.7.8 から）ので、5.6 でも通る形にした。既存の `source` パッケージのテストも `DROP USER` を使っている。
- 初出: B-095
- 再出: 0

## R-122 `testUser` が DB 名の乱数部分を切り詰めて、アカウント名が衝突しうる
- 対象: internal/source/itest_test.go の testUser
- 指摘: 12 桁の 16 進数を 7 から 8 桁に切るので、並行する実行でアカウント名が衝突しうる。
- 却下の理由: 衝突のシナリオを示せない（推測）。8 桁の 16 進数は 32 ビットで、同じサーバーで同時に動くテストの数では衝突しない。5.6 のユーザー名の上限は 16 文字で、切らないと `CREATE USER` が失敗することを実機で確認した（R-073 の前提は 5.7 以降）。
- 初出: B-095
- 再出: 0

## R-123 PostgreSQL 向けの索引名の短縮 `pgShortName` を共有パッケージへ移すべき（SQLite と重複）
- 対象: internal/writer/postgres.go の pgShortName
- 指摘: SQLite ライタも `<table>__<index>` を作るのに短縮が PostgreSQL 専用で、B-097 が再利用するなら共有に移す必要がある。
- 却下の理由: 失敗のシナリオを示せない。SQLite には識別子の長さの上限がなく、短縮は不要。B-097 の外部キー制約名も同じ `writer` パッケージの PostgreSQL ライタ内で呼ぶので、移す必要がない。PostgreSQL 以外のターゲットで必要になった時点で移せばよい。
- 初出: B-096
- 再出: 0

## R-124 統合テスト `TestPostgresLongIndexNames` の正規表現が `pgCells` の引用の形に依存する
- 対象: internal/dump/postgres_test.go の TestPostgresLongIndexNames
- 指摘: `pgCells` が名前を `strconv.Quote` した形で返す前提で、変わるとテストが無関係な理由で落ちる。
- 却下の理由: 推測。`pgCells` は文字列のセルを `strconv.Quote` で返すと決めた共有のヘルパーで、他の統合テストも同じ形（`"child__PRIMARY"` など）を比べている。PostgreSQL 16 の実機で、日本語の索引名（`あ` × 20）を含めて通ることを確認した。
- 初出: B-096
- 再出: 0

## R-125 B-096 の項目が `[todo]` のまま、受け入れ条件が未チェックで差分に入っている
- 対象: BACKLOG.md の B-096
- 指摘: コードとテストが入っているのに、状態とチェックボックスが更新されていない。
- 却下の理由: 手順どおり。`impl-backlog` は、完了の記録（チェックと `# Done` への移動）を検証の後、commit の前に、実装と同じ変更に入れる。レビューの時点では途中の状態を書かない（`BACKLOG.md` に実装中の中間状態は書かない）。
- 初出: B-096
- 再出: 0

## R-126 外部キー名の短縮・警告・重複検出の手順と、主キー名の規則が `WriteSchema` と二重になっている
- 対象: internal/writer/postgres.go の WriteConstraints（`used` と `pgShortName(t.Name+"__PRIMARY")`）
- 指摘: 「短縮、警告、持ち主の記録、重複の検出」が `WriteSchema` の索引名の処理とほぼ同じで、主キー名の組み立ても 2 か所にある。ヘルパーに共通化すべき。
- 却下の理由: 失敗のシナリオを示せない（好みの問題）。短縮の規則そのもの（切り方とハッシュ）は `pgShortName` 1 か所にあり、両方がそれを呼ぶ。重複しているのは数行の手順で、衝突の判定の単位が違う（索引はスキーマ全体の `taken`、制約名はテーブルごとの `used`）ので、共通化するとむしろ引数が増える。主キー名の規則が将来変わったら、`TestPostgresForeignKeyNamedLikeThePrimaryKeyIsAnError` が失敗する。
- 初出: B-097
- 再出: 0

## R-127 主キーが出力されることを確かめずに、`<table>__PRIMARY` を予約している
- 対象: internal/writer/postgres.go の WriteConstraints
- 指摘: `WriteSchema` が主キーを出力しない仕様に変わると、外部キーが不要に失敗する。
- 却下の理由: 推測。今の `WriteSchema` は、主キーが出力できない列を使うときにエラーで止まる（`the primary key uses a column that is not output`）ので、成功した実行では主キーは必ず出力される。仕様が変わったときは、上のテストが失敗して気づく。
- 初出: B-097
- 再出: 0

## R-128 統合テスト `TestPostgresLongForeignKeyName` が `pgCells` の `search_path` に依存している
- 対象: internal/dump/postgres_test.go の TestPostgresLongForeignKeyName
- 指摘: `pgCells` が `search_path` を設定しなければ、`current_schema()` が `public` になって 0 行になる。
- 却下の理由: 確認済み。`pgCells` は先頭で `SET search_path` を設定する（postgres_test.go の pgCells）。統合テストは MySQL 8.0 と 8.4 の両方で、PostgreSQL 17 に対して通った（`task test:integration`）。
- 初出: B-097
- 再出: 0

## R-129 `naming.DataFile` などの単独関数は、表の一覧を知らないので実行時と違う名前を返しうる
- 対象: internal/naming/naming.go の SchemaFile / TriggersFile / ConstraintsFile / DataFile / DataFileParts
- 指摘: 別の表が長いと実行では DB 部分が短縮されるが、1 表だけの `Plan` を作るこれらの関数は短縮していない名前を返す。
- 却下の理由: 本番のコードはこれらを呼ばない（`internal/dump` は一覧から作った `Plan` だけを使う）。使うのはテストで、上限以内の短い名前だけを渡している。上限を超える名前は `Plan` を直接使う（`plan_test.go`）。失敗するシナリオが本番に無い。
- 初出: B-101
- 再出: 0

## R-130 短縮前の仮の名前（1 ファイルの名前）も `seen` に登録されるので、分割された表で大文字小文字の衝突を誤検出しうる
- 対象: internal/dump/dump.go の dumpDB（`claim(seen, dataNames[i], ...)`）
- 指摘: 分割された表では書かれない 1 ファイルの名前を `claim` するので、別 DB の同じ小文字の名前と衝突して誤ってエラーになる。
- 却下の理由: B-101 より前からの設計（仮の名前を先に登録し、パート名は後から登録する）で、この項目の変更ではない。短縮した名前は 16 桁のハッシュを含むので、実際に別の名前と小文字まで一致することは現実には起きない。誤検出は安全側（余計なエラー）で、データは壊れない。
- 初出: B-101
- 再出: 0

## R-131 `TableComponent` が呼ぶたびにエスケープと SHA-256 をやり直す
- 対象: internal/naming/plan.go の TableComponent
- 指摘: 同じ表に対して `TableShortened`・`DataFile`・警告で何度も計算するので、`NewPlan` で 1 回にまとめるべき。
- 却下の理由: 好みの問題。1 表あたり数回、短い文字列の SHA-256 で、実行時間は DB の読み出しに比べて無視できる。キャッシュを足すと `Plan` が状態を持ち、並列の `writeTable`（複数の goroutine が同じ `Plan` を読む）で排他が要る。
- 初出: B-101
- 再出: 0

## R-132 トリガーのテストの正規表現が 2 つのパッケージで重複し、呼ぶたびにコンパイルする
- 対象: internal/source/triggers_test.go の TestTriggers、internal/dump/triggers_test.go の TestTriggersRoundTrip
- 指摘: 名前が囲まれていてもいなくても合う正規表現が 2 パッケージに複製されている。`indexOf` が呼ぶたびにコンパイルする。共通のテスト用ヘルパーにすべき。
- 却下の理由: 好みの問題。2 パッケージのテストは互いに import せず、共有のテスト用パッケージも無い（`isMySQL57` も両方に別々にある）。コンパイルは 1 回の実行で 5 回以下で、無視できる。
- 初出: B-106
- 再出: 0

## R-133 5.7 の修飾付きトリガー名（B-107）があるのに、5.7 の出力を「正しい」と書いている
- 対象: BACKLOG.md の B-106 の結果
- 指摘: 修飾付きの名前が復元できない既知の欠陥があるのに、5.7 のトリガーの出力は正しいと記録している。README の互換性の表との整合も確認すべき。
- 却下の理由: B-106 が確かめたのは、書いたとおりに返る名前（囲み無し）が復元できることで、修飾付きの名前は別の条件（B-107）。結果の文に両方を書き分けてある。README の互換性の表は 5.7 のトリガーを個別のマスにしておらず、変更していない。
- 初出: B-106
- 再出: 0

## R-134 読み取りタイムアウトのヒントが、サーバーが接続を切った場合にも出る
- 対象: internal/source/source.go の Open（`errors.Is(err, mysql.ErrInvalidConn)` でヒントを足す箇所）
- 指摘: `KILL`・`wait_timeout`・再起動でもドライバは `ErrInvalidConn` を返すので、タイムアウトが起きていないのに「read timeout 内に応答しなかったかも」と出て、誤解させる。
- 却下の理由: ドライバは読み取りタイムアウトを `ErrInvalidConn` にまとめて返し、原因を区別できない（ログに `i/o timeout` を出すだけ）。ヒントは「may」と留保し、`--read-timeout` を指定したときだけ付く。何も付けないと、原因の分からない `invalid connection` だけが残る（受け入れ条件「指定時間で失敗する」を使い手が確かめられない）。経過時間での推定は、同じ条件で誤る上に脆い。
- 初出: B-015
- 再出: 1（接続の確立中に起きた `ErrInvalidConn` へのヒントとして。Open のコメントに「may」の理由は書いてある）

## R-135 `--read-timeout` が `--lock-timeout` より短い組み合わせを起動時に拒否・警告すべき
- 対象: cmd/mysqldance/main.go の parseArgsTo（`--read-timeout` の検査）
- 指摘: `--lock-timeout 60s --read-timeout 10s` を許すと、ロック待ちが 10 秒で原因の分からない接続エラーになる。
- 却下の理由: 確定した仕様（B-015）は「README に書く」で、`--read-timeout` は文の種類を問わず「応答を 1 回待つ時間」の上限という単純な意味を保つ。組み合わせの検査は `--lock-timeout 0`（サーバー任せ）との関係も決めることになり、別の仕様。README のフラグ表に、`--lock-timeout` より短くしないよう書いてある。
- 初出: B-015
- 再出: 1（フラグの定義にコメントを足した）

## R-136 `TestOpenConnectTimeout` が到達できないアドレスの扱いにより skip し、接続タイムアウトを検証できない環境がある
- 対象: internal/source/timeout_test.go の TestOpenConnectTimeout
- 指摘: TEST-NET-1 を即座に拒否する環境では skip され、`mc.Timeout` の代入を消してもスイートが通る。
- 却下の理由: skip は意図した動作（拒否する環境では待ちが起きず、検証する対象が無い）。接続の確立全体に効く `ConnectTimeout` は、無応答のサーバーを使う TestOpenConnectTimeoutCoversHandshake が決定的に検証している。
- 初出: B-015
- 再出: 0

## R-137 既定の読み取りタイムアウト 0 がフラグの箇所に直書きで、接続側は定数になっている
- 対象: cmd/mysqldance/main.go の `--read-timeout` の定義
- 指摘: `DefaultConnectTimeout` は定数なのに、読み取り側の既定は 0 の直書きで、2 か所に分かれる。
- 却下の理由: 好みの問題（`CLAUDE.md` や linter の規約に無い）。0 は「制限なし」という値そのもので、既定として選んだ理由（正常な読み出しを打ち切らない）は、フラグの定義の直前のコメントと `Config.ReadTimeout`・`DefaultConnectTimeout` のコメントに書いてある。
- 初出: B-015
- 再出: 0

## R-138 `pingWithin` が `cctx.Err() != nil` で時間切れと判定するので、期限の直前に別の理由で失敗すると誤って時間切れと報告する
- 対象: internal/source/source.go の pingWithin
- 指摘: 認証の拒否が期限とほぼ同時に起きると「接続タイムアウト」と報告される。`errors.Is(err, context.DeadlineExceeded)` で判定すべき。
- 却下の理由: 期限と同じミリ秒に別の失敗が重なる偶然が必要で、起きても 1 回の誤った文言に留まる（元のエラーは `%w` で残る）。ドライバは接続の確立中に ctx が切れると、ctx のエラーではなく `ErrInvalidConn` を返しうるので、`errors.Is` に変えると本物のタイムアウトを見逃す恐れがある（TestOpenConnectTimeoutCoversHandshake が見ているのは、この経路の文言）。
- 初出: B-015
- 再出: 0

## R-139 `mc.Timeout` / `mc.ReadTimeout` を無条件に代入するので、ドライバの既存の設定を 0 で上書きする
- 対象: internal/source/source.go の Open（`mc.Timeout, mc.ReadTimeout = ...`）
- 指摘: DSN の `timeout=` などで設定済みの値を、新しいフィールドの 0 が消す。
- 却下の理由: 事実と違う。`mc` は同じ関数の中で `mysql.NewConfig()` から作り、DSN を読まないので、上書きされる既存の値が無い。0 は「制限なし」で、README とフィールドのコメントに書いてある。
- 初出: B-015
- 再出: 0

## R-140 ヒント、ping のタイムアウト、停止したサーバーでの `--read-timeout` がテストされていない
- 対象: internal/source/timeout_test.go
- 指摘: フラグ解析のテストだけで、`ErrInvalidConn` のヒント、ping のタイムアウトのエラー、停止したサーバーでの挙動は検証されていない。
- 却下の理由: 事実と違う。TestOpenReadTimeout（応答しないサーバー、ヒントの文言、所要時間）、TestOpenConnectTimeoutCoversHandshake（挨拶を返さないサーバー、`--read-timeout` 無し）、TestOpenConnectTimeout（到達できないアドレス）が、どれも失敗を先に確かめてから書いてある。
- 初出: B-015
- 再出: 0

## R-141 `fileStream.add` の行のハッシュと `keyHash` の行の連結が重複している
- 対象: internal/datafile/stream.go の fileStream.add / internal/datafile/datafile.go の keyHash
- 指摘: 「フィールドを `,` でつなぐ」ループが 2 か所にあり、片方だけ変わるとずれる。共通の関数にすべき。
- 却下の理由: 2 つのハッシュは対象のバイト列が違う（`keyHash` は括弧なし、`add` は `(` と `)` 付き）。しかも `add` は書き込みとハッシュと長さの計算を同じループで行うのが、割り当てを減らす（B-017）仕組みそのもの。共通化すると 2 回走査するか、コールバックが要る。ずれは、`TestStreamMatchesInMemoryOutput`（旧実装との出力の比較）と `TestKeyHashMatchesFNV`（`hash/fnv` との比較）が検出する。`)` をハッシュから外す変更を入れると前者が落ちることを確かめた。
- 初出: B-017
- 再出: 0

## R-142 `fileStream.add` が途中の `WriteString` のエラーを見ない
- 対象: internal/datafile/stream.go の fileStream.add
- 指摘: フィールドの書き込みのエラーを捨て、最後の `)` の書き込みだけ確かめている。`bufio.Writer` でなくなると、書き込みの失敗を見逃す。
- 却下の理由: `f.bw` は具体型の `*bufio.Writer` で、最初のエラーを保持し、以降の書き込みはそのエラーを返す（標準ライブラリの仕様）。最後の書き込みで必ず見える。型が変わる将来の変更は、コンパイルの時点では気づかれないが、コメントに前提を書いてある。失敗のシナリオは、今のコードでは示せない。
- 初出: B-017
- 再出: 0

## R-143 `f.size += n + 2` の `2` と `n` の初期値の `2` が紛らわしい
- 対象: internal/datafile/stream.go の fileStream.add
- 指摘: `n` の初期値の `2` は括弧、`+ 2` は区切り（`,\n` / `;\n`）で、読み違えると `MaxInsertBytes` の区切りが動く。
- 却下の理由: 好みの問題（コメントの書き方）。動作は旧実装（`len(text) + 2`）と同じで、`MaxInsertBytes` の区切りは `TestStreamMatchesInMemoryOutput` が旧実装と比べている。理由を 1 行のコメントで足した。
- 初出: B-017
- 再出: 0

## R-144 `fileStream.add` のハッシュを `hash/fnv` と直接比べるテストがない
- 対象: internal/datafile/stream_test.go / bench_test.go
- 指摘: `add` の行のハッシュは、旧実装との出力の比較でしか確かめられていない。`)` を落とすなどの誤りを見逃すかもしれない。
- 却下の理由: 事実と違う。`)` をハッシュから外す変更を入れて `go test ./internal/datafile` を実行すると、`TestStreamMatchesInMemoryOutput/defaults` が落ちる（B-017 で確認した）。`legacyWriteStatements` は `hash/fnv` を使う旧実装そのもの。
- 初出: B-017
- 再出: 0

## R-145 型名の写像表がドライバの名前と合うかは、統合テストを実行しないと分からない
- 対象: internal/source/source.go の catalogTypeNames
- 指摘: 写像表の名前がドライバの `DatabaseTypeName` と違うと、`sqlvalue.Literal` が黙って別の分岐に進み、出力が変わる。
- 却下の理由: 推測ではなく確認済み。`TestTableInfoColumnTypes` が全型の列を持つ表で、写像の結果と `columnTypes`（ドライバの名前）を列ごとに比べる。MySQL 8.0 / 8.4 と MariaDB 10.11 で通った（B-019）。このテストで TEXT / BLOB が 4 つの大きさとも 1 つの名前になることも見つかり、写像に反映した。
- 初出: B-019
- 再出: 0

## R-146 `tableListed` は不要な状態で、DB 名をキーにすべき
- 対象: internal/source/source.go の Snapshot.tableList / tableListed
- 指摘: 将来 `s.name` を `use` を通さずに書き換えると、古い一覧を返す。
- 却下の理由: 推測。`s.name` を書き換えるのは `use` だけで、そこで一覧を捨てる（`TestListTablesFollowsUse`）。空の一覧を「未取得」と区別するのに `tableListed` が要る。
- 初出: B-019
- 再出: 0

## R-147 `catalogTypeName` の整数の型の一覧が写像表と二重になっている
- 対象: internal/source/source.go の catalogTypeName の switch
- 指摘: 整数の型を写像表に足して switch に足し忘れると、UNSIGNED の接頭辞が付かない。
- 却下の理由: 仮定の話で、今の失敗は示せない。整数の型は固定（5 種類）で、`TestCatalogTypeName` と `TestTableInfoColumnTypes`（UNSIGNED と ZEROFILL の列）が全部を確かめる。
- 初出: B-019
- 再出: 0

## R-148 `tableColumns` が、写像にない型があるとき捨てる結果も作る
- 対象: internal/source/source.go の tableColumns
- 指摘: 1 列でも写像にないと全体を `LIMIT 0` に戻すので、先に作った結果が無駄になる。
- 却下の理由: 好みの問題。無駄は列数に比例する割り当て 1 回で、`LIMIT 0` の往復に比べて無視できる。全体を戻すのは確定した仕様（黙って別の名前にしない）。
- 初出: B-019
- 再出: 0

## R-149 `viewForeignRefs` の文字列探索が、大文字小文字だけが違う DB 名や別名・リテラルで偽の警告を出す
- 対象: internal/source/dialect.go の viewForeignRefs
- 指摘: `Sales` と `sales` が両方ある大文字小文字を区別するサーバーで、自分の DB の修飾子が他方に一致する。別名や文字列リテラルでも一致する。
- 却下の理由: 結果は警告が 1 件増えるだけで、出力（ファイル）も順序も変わらない。大文字小文字を区別せずに探すのは、`lower_case_table_names=2` の定義の綴り違いで参照を見落とさないための確定した仕様で、`viewRefs` と同じ（R-062）。大文字小文字だけが違う 2 つの DB を持つ構成は稀。コメントに書いた。
- 初出: B-023
- 再出: 0

## R-150 `viewTableUsage` の `TABLE_SCHEMA = ?` の照合順序が、サーバーの DB 名の大小の扱いと違いうる
- 対象: internal/source/dialect.go の viewTableUsage
- 指摘: `lower_case_table_names=0` で `pr` と `PR` があるとき、同じ DB と見なされ、警告もビューの依存も誤る。
- 却下の理由: この比較は B-023 より前（`TABLE_SCHEMA = ?`）からある挙動で、この項目の変更ではない。誤りの影響は警告の見落としと、自分の DB のビュー名に一致するときだけの依存の追加で、出力の欠落や破損はない。実機で再現できておらず（推測）、そのようなサーバー構成は想定外。
- 初出: B-023
- 再出: 0

## R-151 別の DB を読むビューの警告が、ファイル名の順序にかかわらず常に出る
- 対象: internal/writer/mysql.go の WriteSchema
- 指摘: 参照先が先に復元される順序でも警告が出て、雑音になる。
- 却下の理由: 確定した仕様。復元先に何があるかは `mysqldance` には分からず、参照先がダンプに含まれない場合や復元先にだけある場合と区別できないため、常に出す（設計書 11 節の B-023【決定】、`TestViewReadingAnotherDatabase` の「sorts first」）。
- 初出: B-023
- 再出: 0

## R-152 別の DB を読むビューの警告が MySQL ライタだけにあり、SQLite / PostgreSQL では黙って落ちる
- 対象: internal/writer/mysql.go の WriteSchema と `schemafile.View.Reads`
- 指摘: 他のライタが `Reads` を見ないので、警告なしにビューが壊れる。
- 却下の理由: 前提が事実と違う。SQLite と PostgreSQL のライタはビューを出力せず、ビューごとに「not output」の警告を出す（sqlite.go の `views are not converted to SQLite`、postgres.go の `views are not converted to PostgreSQL`）。壊れるビューは出力に存在しない。
- 初出: B-023
- 再出: 0

## R-153 `schemaNames` が DB ごとに `SCHEMATA` を引き、ビュー × DB 名の探索になる
- 対象: internal/source/dialect.go の viewDefinitions / schemaNames
- 指摘: 数千の DB があるサーバーで、DB ごとに全件の一覧を引き、O(ビュー数 × DB 数) の探索になる。
- 却下の理由: 効率だけの指摘で、失敗のシナリオはない。問い合わせは MariaDB / 5.7 / 5.6 で、ビューがある DB ごとに 1 回。想定の規模（MVP は小さなサーバー）の外で、`Snapshot` に一覧を持たせると `Use` で捨てる状態が増える（R-007 と同じ理由）。
- 初出: B-023
- 再出: 0

## R-154 `Sys` など、システムスキーマと同じ名前の利用者の DB が警告から外れる
- 対象: internal/source/dialect.go の systemSchemas
- 指摘: 利用者の DB がシステムスキーマと同名だと、警告が出ない。
- 却下の理由: `mysql` / `sys` / `information_schema` / `performance_schema` はサーバーが持つ名前で、同名の利用者の DB を作ることはできない（作れても、それはサーバーのものと同じ DB）。失敗のシナリオを示せない。
- 初出: B-023
- 再出: 0

## R-155 `foreign` の結果が `viewDep` を流用し、`ref` が名前でなく DB 名の意味になる
- 対象: internal/source/dialect.go の viewDeps と viewDep
- 指摘: 別の型か、ビュー → 並べた名前の map の方が分かりやすく、`Views()` のループも減る。
- 却下の理由: 好みの問題で、失敗のシナリオはない。`viewDep` と `viewDeps` のコメントに、`foreign` では `ref` が DB 名だと書いてある。呼び出しは `Views()` の 1 か所だけ。
- 初出: B-023
- 再出: 0

## R-156 パスワードファイルの警告が `--log-level error` で隠れる
- 対象: cmd/mysqldance/main.go の readPasswordFile
- 指摘: CI でよくある `--log-level error` だと警告が出ず、README も常に出るように読める。
- 却下の理由: 警告は全て `WARN` 記録で、`error` が隠すことは README の `--log-level` の行と「Warnings」節に明記済み（他の警告と同じ扱い）。パスワード用だけレベルを迂回する仕様は確定していない。README は「常に出る」とは書いていない。
- 初出: B-024
- 再出: 0

## R-157 警告が引数解析の副作用で出る。`readPasswordFile` が読みとログを兼ねる
- 対象: cmd/mysqldance/main.go の parseArgsTo / readPasswordFile
- 指摘: `parseArgs` だけを呼ぶ検証や将来の dry-run でも警告が出る。モードを返して `run()` でログすべき。
- 却下の理由: 失敗のシナリオが推測のみ（dry-run や validate モードは存在しない）。パスワードの読み込みは今も `parseArgsTo` の中にあり、同じ場所で警告するのが最も単純。`parseArgsTo` がロガーを作る箇所はここだけで、`run()` へ移すと読み込みを分ける必要が出る。
- 初出: B-024
- 再出: 0

## R-158 `Stat` の失敗を握りつぶす。書き込み権限は見ない。FIFO・ディレクトリのテストがない
- 対象: cmd/mysqldance/main.go の readPasswordFile、main_test.go の TestParseArgsPasswordFileMode
- 指摘: `Stat` が失敗すると検査が黙って飛ぶ。グループ・他人が書けるファイルを検査しない。`/dev/fd/N` などのテストがない。
- 却下の理由: 開いた直後のファイルの `Stat` が失敗するのは実際には起きず、失敗してもパスワードの読み込み（本体）は続き、データは落ちない（「黙って落とさない」は出力データの話）。読み取りビットだけを見るのは B-024 で確定した仕様（受け入れ条件は「読める」）。存在しないファイルは `TestParseArgsInvalid` の "missing password file" で、ディレクトリは `ReadAll` のエラーで失敗する。`/dev/fd/N` の誤警告は再現するシナリオを示せない（パイプはオーナーのみ 0600）。
- 初出: B-024
- 再出: 0

## R-159 `:main` の push で arm64 を実行せず、`:main-<arch>` の途中のタグが残る
- 対象: .github/workflows/image.yml の Smoke test と Push :main
- 指摘: arm64 のイメージは実行して確かめないまま push される。`:main-amd64` / `:main-arm64` が ghcr に残る。
- 却下の理由: arm64 の実行には QEMU が要り、イメージはビルド済みの静的バイナリを COPY するだけ（アーキテクチャごとに goreleaser が `GOARCH` を指定して作る）。壊れる具体的なシナリオを示せない。途中のタグが残るのは、OSS 版の goreleaser に `--nightly` が無いための設計（B-028 の確定した仕様に記載）。
- 初出: B-028
- 再出: 0

## R-161 tag.yml のイメージに smoke test が無い
- 対象: .github/workflows/tag.yml
- 指摘: タグのリリースで push するイメージを、実行して確かめていない。
- 却下の理由: 同じ Dockerfile と設定のイメージを、ブランチと `main` で毎回ビルドして `--version` を実行している（image.yml）。タグ専用の確認を足す具体的な失敗シナリオを示せない。
- 初出: B-028
- 再出: 0

## R-162 Dockerfile に `WORKDIR` / `VOLUME` が無く、相対パスの出力先が書けない
- 対象: Dockerfile
- 指摘: nonroot（uid 65532）で動くので、マウントの権限や相対パスの `-o` で失敗する。
- 却下の理由: distroless では書き込み可能なディレクトリを用意するのに追加の手間（`COPY --chown`）が要り、マウントの権限は利用者の環境による。README に「uid 65532 が書けるディレクトリを渡す」と明記し、例は絶対パスにしている。
- 初出: B-028
- 再出: 0

## R-163 BACKLOG の `:main-<arch>` の設計が goreleaser の設定と食い違う
- 対象: BACKLOG.md の B-028、.goreleaser.yaml
- 指摘: 設定にアーキテクチャ別のタグが無い。
- 却下の理由: `goreleaser release --snapshot` は `dockers_v2` の設定からアーキテクチャ別のイメージ（`<tag>-amd64` / `<tag>-arm64`）をローカルに作る。実機で確かめた（`docker run` で `--version` も確認）。
- 初出: B-028
- 再出: 0

## R-164 `extsort` の `mergePass` が、失敗したとき group の所有を呼び出し先に渡す（壊れやすい不変条件）
- 対象: internal/extsort/extsort.go の mergePass と mergeRuns
- 指摘: `mergeRuns` は失敗しても group の塊を全て削除するので、後で group を再利用すると二重に削除する。マージの途中で失敗するテストが無い。
- 却下の理由: 失敗のシナリオを示せない（レビュー自身が、経路を追っても具体的な失敗は見つからなかったと書いている）。`Remove` は何度呼んでも安全で、`out` は先に `s.runs` へ入れるので `Close` が消す。所有の取り決めは `mergePass` のコメントに書いた。
- 初出: B-045
- 再出: 0

## R-165 `--sort-memory=`（空）が使い方のエラーにならず、既定値になる
- 対象: cmd/mysqldance/main.go の `--sort-memory` の解析
- 指摘: 空の値は黙って 64MiB になり、「黙って落とさない」に反する。
- 却下の理由: `--file-mode` と `--dir-mode` が同じ扱い（空は未指定）で、フラグの規約として揃えた。不正な値（単位の誤り、1MiB 未満、負数）は全て使い方のエラーになる。丸めるのは未指定を表す空文字だけで、データを落とさない。コメントに R-165 を書いた。
- 初出: B-045
- 再出: 0

## R-166 `dumpDB` のルーチンの処理がトリガーの処理のコピーになっている
- 対象: internal/dump/dump.go の dumpDB（`writer.RoutineWriter` と `writer.TriggerWriter` の分岐、claim と書き出し）
- 指摘: 読む、ライタの型を確かめる、オブジェクトごとに警告する、nil にする、の並びが重複している。3 つ目の種類（B-048 のイベント）で 3 つ目のコピーになる。
- 却下の理由: 失敗のシナリオを示せない（好みの問題）。種類ごとに警告の属性（`table` / `trigger` と `routine` / `type`）、ファイル名、ライタのインターフェースが違い、共通化するとコールバックだらけになる。2 つの間の重複は短く、`dumpDB` の分割は B-050 で扱う。
- 初出: B-047
- 再出: 0

## R-167 ルーチンの種類の検査が `source.Routines` と `schemafile.WriteRoutines` の両方にある
- 対象: internal/source/routines.go の Routines、internal/schemafile/routines.go の WriteRoutines
- 指摘: 同じ検査が 2 か所にあり、メッセージが違う。許す種類を変えるときに両方を直す必要がある。
- 却下の理由: 失敗のシナリオを示せない。`source` の検査は未対応の種類（MariaDB のパッケージ）を黙って落とさないため、`schemafile` の検査は `DROP` / `CREATE` の文を作るときに種類を文に入れてよいと保つため。層が別で、どちらか一方だけでも誤った入力は止まる。`TestWriteRoutinesRefusesUnknownType` が確かめている。
- 初出: B-047
- 再出: 0

## R-168 `showCreateStatement` の `kind` を SQL に連結している
- 対象: internal/source/triggers.go の showCreateStatement
- 指摘: `"SHOW CREATE "+kind` は呼び出し側が定数を渡す前提で、将来の呼び出しで注入になりうる。
- 却下の理由: 失敗のシナリオを示せない。呼び出しは `"TRIGGER"` の定数と、`Routines` が `PROCEDURE` / `FUNCTION` に絞った値だけで、利用者の入力は入らない。`SHOW CREATE` の種類名は `?` で渡せない。メソッドは非公開で、コメントに定数を渡すと書いた。
- 初出: B-047
- 再出: 0

## R-169 ルーチン用に書き換えたコメントが長い行になった
- 対象: internal/naming/plan.go の IsOwned のコメント、internal/source/triggers.go の showCreateTrigger のコメント
- 指摘: コメントが長くなり、`showCreateTrigger` のコメントから「列は名前で選ぶ」の説明が消えた。
- 却下の理由: 好みの問題で、`CLAUDE.md` や linter の規約にない（`golangci-lint` は通っている）。列の説明は共通の `showCreateStatement` のコメントに残っている。
- 初出: B-047
- 再出: 0

## R-170 イベントの `SET TIME_ZONE` を、最後に `+00:00` へ戻さない
- 対象: internal/schemafile/events.go の WriteEvents
- 指摘: 最後のイベントのタイムゾーンがセッションに残り、後続の文が非 UTC で動く。名前のゾーンは復元先にゾーン表がないと失敗する。
- 却下の理由: 失敗のシナリオを示せない。後続の文は無い（ファイルの末尾は `DELIMITER ;` だけ）。他のファイルは各自のヘッダーで `SET TIME_ZONE='+00:00'` を先頭に流すので、続けて復元しても持ち越されない。`sql_mode` もルーチン・トリガーで戻していない。名前のゾーンの前提は README の Events 節に書いてある。
- 初出: B-048
- 再出: 0

## R-171 `dumpDB` が、ライタがイベントを書けるかを見る前にイベントを読む
- 対象: internal/dump/dump.go の dumpDB（snap.Events）
- 指摘: `EventWriter` でないライタでも `SHOW CREATE EVENT` を 1 件ずつ実行し、読めないイベントがあると警告ではなくエラーで止まる。
- 却下の理由: トリガーとルーチンの処理と同じ並びで、台帳 R-166 と同じ理由（種類ごとの共通化は B-050 で扱う）。読めないものを警告に落とさず止めるのは、「黙って落とさない」の安全側の挙動。
- 初出: B-048
- 再出: 0

## R-172 `showCreateColumns` が NULL と空文字を区別しない
- 対象: internal/source/triggers.go の showCreateColumns
- 指摘: `time_zone` が NULL でも空文字になり、原因が分かりにくい。
- 却下の理由: 失敗のシナリオを示せない。NULL の `time_zone` は空文字になり、`WriteEvents` が「time zone がない」というエラーで止める（`TestWriteEventsRefusesNoTimeZone`）。黙って落ちない。文の列だけが NULL を権限の不足として扱うのは、その列だけが権限で NULL になるため。
- 初出: B-048
- 再出: 0

## R-173 イベントのファイルの古いものが、名前の短縮で残る
- 対象: internal/dump/dump.go のイベントファイルの出力と古いファイルの削除
- 指摘: データベース名が短縮される場合、`isOwned` を通らない削除経路で前回のイベントのファイルが残り、削除したイベントが復元で戻る。
- 却下の理由: 削除の経路は `removeStale` の 1 つで、`Plan.IsOwned` が短縮の有無どちらの形も認識する（`TestPlanShortensLongDatabaseForAllFiles`）。イベントのファイルは `TestEventsFile` と `TestNoEventsNoFileAndStaleFileRemoved`（統合）で確かめている。`isOwned` を通らない経路は無い。
- 初出: B-048
- 再出: 0

## R-174 `table dumped` が、後続の検査が終わる前に出る
- 対象: internal/dump/dump.go の writeTable（`table dumped`）
- 指摘: 部品の名前の衝突や他のテーブルの失敗で、そのあと実行が失敗しても、`table dumped` は出てしまう。
- 却下の理由: 確定した仕様（B-053 の前提）。`table dumped` は進捗で、書き終えた順に出る。仮の名前で書いた時点を指し、入れ替えの前であることは README の「Progress」節に書いた。失敗した実行には `database dumped` が出ない（`TestProgressLogNotDoneOnFailure`）ので、完了の印にはならない。
- 初出: B-053
- 再出: 0

## R-175 `dumpDB` が集計を出力引数で返している
- 対象: internal/dump/dump.go の dumpDB（`tot *dbTotals`）
- 指摘: 戻り値で返すほうが、契約が明示的になる。
- 却下の理由: 好みの問題。呼び出し側は成功したときだけ読み、`dumpDB` は `seen`・`plans` を既に出力引数で渡している。`return err` が 30 近くあり、戻り値に変えると本項目と関係のない差分が膨らむ（段階ごとの関数に分ける B-050 で扱う）。
- 初出: B-053
- 再出: 0

## R-176 DB の `duration` が、テーブルの `duration` の合計と一致しない
- 対象: internal/dump/dump.go の Run（`database dumped`）
- 指摘: DB の所要時間にスキーマ・ルーチンなどの読み出しが入るので、テーブルの合計と比べられない。
- 却下の理由: 仕様どおり（DB の開始から終了まで）。並列のテーブルの時間は重なるので、合計と一致する必要はない。README の「Progress」節に、DB の `duration` にスキーマの読み出しが含まれることを書いた。
- 初出: B-053
- 再出: 0

## R-177 `countingFile` が、元のライタの他のメソッドを隠す
- 対象: internal/dump/dump.go の countingFile
- 指摘: 将来、書き出し側が `io.StringWriter` などの任意のインターフェースへ型アサーションすると、黙って遅い経路になる。
- 却下の理由: 失敗のシナリオを示せない（推測）。`NewRowWriter` が受け取るのは `io.WriteCloser` で、`internal/writer`・`internal/datafile`・`internal/dump` に型アサーションは無い（`rg` で確認した）。
- 初出: B-053
- 再出: 0

## R-178 `extraFiles` が `planFiles` と `writeExtraFiles` で 2 回評価される
- 対象: internal/dump/dump.go の extraFiles / planFiles / writeExtraFiles
- 指摘: 名前の確保と書き出しで別々に評価されるため、将来どちらかだけ条件を変えると、確保していないファイルを書く、または確保して書かないことになる。
- 却下の理由: 失敗のシナリオを示せない（推測）。`extraFiles` は同じ入力（ライタ、`schema`、`plan`）だけに依存する純粋関数で、両者は同じ値を見る。関数のコメントにその旨を書いた。評価のコストは DB ごとに数個のクロージャで無視できる。
- 初出: B-050
- 再出: 0

## R-179 `dumpDB` の分割で `plans[db]` が `planFiles` の成功後にしか入らない
- 対象: internal/dump/dump.go の dumpDB（`plans[db] = plan`）
- 指摘: 名前の衝突で失敗したとき、以前は入っていた `plans` の要素が無くなる。
- 却下の理由: `Run` は `dumpDB` のエラーですぐに返り、`plans` を読むのは全 DB の書き出しが終わった後だけ（`ownedEntries` と `removeStale`）。失敗後に `plans` を使う経路は無く、挙動と出力は変わらない。`dumpDB` にコメントを書いた。
- 初出: B-050
- 再出: 0

## R-180 ライタの対応の確認が `readSchema` と `extraFiles` の 2 か所にある
- 対象: internal/dump/dump.go の readSchema / extraFiles
- 指摘: 「ライタが対応していて、中身がある」という判断が 2 か所に分かれ、種類を足すと両方を直す必要がある。
- 却下の理由: 失敗のシナリオを示せない（好みの問題）。`readSchema` の役割は読んだものを落として警告すること、`extraFiles` の役割はファイルの一覧で、問いが違う。分けたままでも、片方を直し忘れたとき、警告が出るのに書かれる（またはその逆）ことは、既存のテストが捕まえる。R-166 と同じ理由で、共通化するとコールバックだらけになる。
- 初出: B-050
- 再出: 0

## R-181 `readSchema` / `writeTables` / `writeExtraFiles` が `Options` を丸ごと受け取る
- 対象: internal/dump/dump.go の readSchema、writeTables、writeExtraFiles
- 指摘: 必要なのは `Log`、`Writer`、`OutDir` だけで、引数が広く、`writeTables` は 9 個になる。
- 却下の理由: 保守性の好みで、失敗のシナリオが無い。引数を狭める構造体の導入は B-051（`Options` への置き換え）で触る範囲と重なるので、そこで扱う。この項目は挙動を変えない分割に限る。
- 初出: B-050
- 再出: 0
