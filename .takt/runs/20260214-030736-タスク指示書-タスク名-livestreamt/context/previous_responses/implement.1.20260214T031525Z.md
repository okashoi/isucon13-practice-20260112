実装タスクを開始します。まず指定どおり Report Directory の一次情報を確認し、同時に `context/task/order.md` を読み直して、変更対象とテスト要件を確定します。
一次情報として `reports/00-plan.md` を読み、実装指示の具体内容を固定します。その後、着手要件どおり Scope ファイルを先に作成します。
`rg` が使えないため `find`/`grep` に切り替えて対象コードを抽出します。次に Scope 宣言を先に作成してから実装へ入ります。
これから Scope 出力契約に沿って `02-coder-scope.md` を先に作成し、その後 `go/livestream_handler.go` と関連型定義を読んで差分実装に入ります。
`livestream_handler.go` の読み書き経路を確認できました。次にタグ型定義を確認し、キャッシュ型を破綻なく追加できる形で新規ファイルとハンドラ修正を入れます。
実装編集に入ります。まず `go/livestream_tag_cache.go` を新規作成して、60秒TTL・スレッドセーフな `get/set/invalidate` と、DBフォールバック付きの取得ヘルパーを定義します。
次に `go/livestream_handler.go` を編集し、`search` のタグ経路をキャッシュ利用へ置換、`fillLivestreamsResponse` のタグ取得を共通キャッシュヘルパーへ統一、更新後 invalidate を追加します。
整合性を保つため、初期化 API 実行時に `livestreamTags` キャッシュもクリアする1行を追加します。続けて単体テストを実装します。
`go/livestream_tag_cache_test.go` を新規作成し、TTL(60秒)・更新反映/無効化・同時実行の3観点をカバーする単体テストを追加します。
実装とテスト追加は反映済みです。ここから必須のテスト実行として `go test ./...` を `go` ディレクトリで実行し、失敗があれば修正します。
テスト実行がサンドボックス制約で失敗したため、ツールチェーン自動ダウンロードを無効化して再実行します。必要ならワークスペース内に Go キャッシュを切って回避します。
レポート整合性のため、実際に変更した `go/main.go` を Scope 宣言にも追記します。あわせて決定ログファイルを作成して実装完了状態にします。
## 作業結果
- `livestreamTags` 向けのオンメモリキャッシュ（TTL 60秒、スレッドセーフ）を実装し、参照経路・更新経路へ組み込みました。
- Report Directory に Scope と Decisions を作成しました。  
  - `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/reports/02-coder-scope.md`  
  - `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/reports/03-coder-decisions.md`

## 変更内容
- 作成: `go/livestream_tag_cache.go`
  - `livestreamTagCacheTTL = 60 * time.Second` を定義 (`go/livestream_tag_cache.go:11`)
  - `get/set/invalidate/clear` を持つキャッシュ実装 (`go/livestream_tag_cache.go:33`, `go/livestream_tag_cache.go:54`, `go/livestream_tag_cache.go:63`, `go/livestream_tag_cache.go:69`)
  - キャッシュ優先 + ミス時DB読込の `getLivestreamTagsMap` を実装 (`go/livestream_tag_cache.go:86`)
- 変更: `go/livestream_handler.go`
  - 予約作成コミット後に対象配信のキャッシュ無効化を追加 (`go/livestream_handler.go:169`)
  - `searchLivestreamsHandler` のタグ検索経路を `livestream_tags` 直読からキャッシュ経由判定に変更 (`go/livestream_handler.go:185`)
  - `fillLivestreamsResponse` のタグ解決を `getLivestreamTagsMap` 利用に変更 (`go/livestream_handler.go:543`)
- 変更: `go/main.go`
  - `/api/initialize` 実行時に `livestreamTags` キャッシュをクリアする処理を追加 (`go/main.go:121`)
- 作成: `go/livestream_tag_cache_test.go`
  - TTL挙動テスト (`go/livestream_tag_cache_test.go:9`)
  - 更新反映/無効化テスト (`go/livestream_tag_cache_test.go:37`)
  - 同時実行テスト (`go/livestream_tag_cache_test.go:61`)
  - キャッシュ値の防御的コピー確認テスト (`go/livestream_tag_cache_test.go:82`)

## テスト結果
- 実行コマンド:
  - `GOPATH=/tmp/gopath GOMODCACHE=/tmp/gopath/pkg/mod GOCACHE=/tmp/go-build-cache GOTOOLCHAIN=auto go test ./...`
- 結果:
  - `ok   github.com/isucon/isucon13/webapp/go  0.204s`