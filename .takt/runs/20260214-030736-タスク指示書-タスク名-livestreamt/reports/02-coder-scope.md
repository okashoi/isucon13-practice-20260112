`livestreamTags` のオンメモリキャッシュ実装（TTL 60秒・更新後整合性）を完了しました。

実施内容:
- `go/livestream_tag_cache.go` を新規作成
  - `livestreamTagCacheTTL = 60 * time.Second`
  - スレッドセーフな `get/set/invalidate/clear`
  - キャッシュ優先・ミス時DB読込の `getLivestreamTagsMap`
- `go/livestream_handler.go` を更新
  - `fillLivestreamsResponse` のタグ取得をキャッシュ経由に統一
  - `searchLivestreamsHandler` のタグ検索経路をキャッシュ利用に変更
  - `reserveLivestreamHandler` のDBコミット成功後に `livestreamTagsCache.invalidate(livestreamID)` を追加
- `go/main.go` を更新
  - `initializeHandler` で `livestreamTagsCache.clear()` を追加
- `go/livestream_tag_cache_test.go` を新規作成
  - TTL挙動テスト
  - 更新反映/無効化テスト
  - 同時実行時の整合性テスト
  - 参照値コピー（破壊的変更防止）テスト

テスト:
- 実行コマンド:
  - `GOPATH=/tmp/gopath GOMODCACHE=/tmp/gopath/pkg/mod GOCACHE=/tmp/go-build-cache GOTOOLCHAIN=auto go test ./...`
- 結果:
  - `ok github.com/isucon/isucon13/webapp/go 0.204s`

レポート出力:
- `02-coder-scope.md` を作成・更新
- `03-coder-decisions.md` を作成