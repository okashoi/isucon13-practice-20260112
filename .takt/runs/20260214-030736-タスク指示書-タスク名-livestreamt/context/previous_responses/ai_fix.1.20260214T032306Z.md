タスク指示に従い、まず `.takt/runs/.../context/task` と Report Directory の一次情報を読み、指摘されたファイルと問題箇所をゼロベースで事実確認します。続けて該当箇所を修正し、テスト実行まで行います。
`rg` が使えない環境なので、`find`/`grep` で代替して一次情報を列挙します。次にタスク指示書・ポリシー全文・レポートを順に読みます。
レポート一次情報（特に `04-ai-review.md`）と `order.md`、`ai_fix` ポリシー全文を開いて、修正対象を確定します。
AI Reviewで指摘された `go/livestream_handler.go` と `go/main.go` を行番号付きで開き、次に `grep` で全件走査クエリとキャッシュ無効化経路の実在を確認します。確認後に必要な修正を入れます。
検索結果を確認し、`AIREV-002` の全件走査クエリは実在、`AIREV-001` のハンドラテストは未整備であることを確認しました。次に既存コードを読み、最小変更で検索SQL修正とハンドラ回帰テストを追加します。
次にハンドラテストを追加するため、`dbConn` の定義と既存のレスポンス組み立て依存（`fillLivestreamsResponse` など）を確認します。テストが通る最小のDBシードで組みます。
これから `go/livestream_handler.go` を編集し、タグ検索をDB絞り込みSQLに戻して全件走査ロジックを削除します。続けて不足しているハンドラ回帰テストを追加します。
ここからテストを追加します。`AIREV-001` の3点（検索経路、予約後 invalidate、initialize 後 clear）をハンドラ実行で検証する `go` テストを新規作成し、`sqlmock` でDB依存を固定化します。
`go/livestream_handler_test.go` を追加し、3つのハンドラ経路をテスト化しました。これから `gofmt` と `go test` を実行して不足依存や期待値ズレを解消します。
テスト実行で `sqlmock` 依存不足を確認しました。次に `go/go.mod` に依存を追加し、再度テストを通します。
実装とテストは通過しました。最終報告用に、実際に確認した行番号・検索コマンド・変更差分を抜き出して整理します。
## 確認したファイル
- `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/task/order.md:1`
- `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/reports/04-ai-review.md:1`
- `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/policy/ai_fix.1.20260214T031730Z.md:1`
- `go/livestream_handler.go:185`
- `go/livestream_handler.go:194`
- `go/livestream_handler.go:201`
- `go/main.go:121`
- `go/livestream_tag_cache_test.go:9`
- `go/livestream_handler_test.go:27`
- `go/livestream_handler_test.go:114`
- `go/livestream_handler_test.go:205`

## 実行した検索
- `grep -RIn "SELECT \* FROM livestreams ORDER BY id DESC" go`  
  - `go/livestream_handler.go:194` にタグ検索時の全件走査が実在することを確認。
- `grep -RIn "searchLivestreamsHandler\|initializeHandler\|reserveLivestreamHandler\|livestreamTagsCache" go/*_test.go go`  
  - ハンドラ回帰テストが不足し、キャッシュ単体テスト中心であることを確認。
- `ls go/*_test.go`  
  - 既存テストが `go/livestream_tag_cache_test.go` のみであることを確認。

## 修正内容
- `AIREV-002` 対応  
  - `go/livestream_handler.go:194` を確認した結果、タグ検索で全件取得していたため、`SELECT DISTINCT l.* ... INNER JOIN livestream_tags ... WHERE lt.tag_id IN (?) ORDER BY l.id DESC` に修正しました。  
  - 併せてアプリ側フィルタ処理（全件タグ判定ロジック）を削除しました。
- `AIREV-001` 対応  
  - `go/livestream_handler.go:185` / `go/livestream_handler.go:201` / `go/main.go:121` の新挙動に対し、ハンドラ回帰テストを `go/livestream_handler_test.go` に追加しました。  
  - 追加テスト:
    - `TestSearchLivestreamsHandler_WithTagUsesDBFilterAndReturnsTaggedLivestreams`（タグ検索経路）
    - `TestReserveLivestreamHandler_InvalidatesTagCacheAfterCommit`（予約後 invalidate）
    - `TestInitializeHandler_ClearsLivestreamTagCache`（initialize 後 clear）
  - `initializeHandler` をテスト可能にするため、`go/main.go:33` に `runInitScript` / `triggerPProteinCollect` の差し替え可能な関数変数を追加しました（本番挙動は同一）。
- テスト依存追加  
  - `go/go.mod:21` に `github.com/DATA-DOG/go-sqlmock v1.5.2` を追加（`go/go.sum` 更新）。

## テスト結果
- 実行コマンド:  
  - `GOTOOLCHAIN=auto GOPATH=/tmp/gopath GOMODCACHE=/tmp/gomodcache GOCACHE=/tmp/gocache GOTMPDIR=/tmp go test ./...`（`go` ディレクトリで実行）
- 結果:  
  - `ok github.com/isucon/isucon13/webapp/go 0.214s`