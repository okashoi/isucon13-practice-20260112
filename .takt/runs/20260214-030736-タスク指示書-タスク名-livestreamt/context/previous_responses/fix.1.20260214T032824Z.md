依頼内容に沿って、まず Report Directory の一次情報（`reviewers` レポートと `order.md`、`policy` 原文）を確認し、修正対象を特定します。対象を確定後、必要な修正を実装してテストまで実行します。
`rg` が利用できない環境だったので、`find` で同じ情報を取得します。続けてレビュー指摘ファイルを特定します。
対象レポートを特定できたので、`order.md` と `05-architect-review.md` / `06-qa-review.md` を一次情報として読み、修正指示を抽出します。
修正対象は `QAREV-001` の1件です。これから実装ファイルと既存テストを読み、`getLivestreamTagsMap` の「ミス時DB取得→キャッシュ格納→2回目ヒット」「DB取得失敗時エラー伝播」を満たすテストを追加します。完了後に `go test ./...` で検証します。
既存実装を確認したので、`go/livestream_tag_cache_test.go` に統合テストを追加します。`getLivestreamTagsMap` を直接呼び、1回目ミス時のDB参照・2回目ヒット時のDB未参照と、DBエラー伝播を検証する形で修正します。
パッチ適用が失敗したため、先頭の import ブロックを再確認してから差分を正確に当て直します。
テスト追加を反映しました。これから `go test` を実行して、追加した2ケースを含めて回帰が通ることを確認します。
## 作業結果
- `06-qa-review.md` の指摘 `QAREV-001` に対応し、`getLivestreamTagsMap` の未検証だったDB連携経路のテストを追加しました。
- 追加テストで「1回目ミス時にDB取得してキャッシュ保存」「2回目はキャッシュヒットでDB未参照」「DB取得失敗時のエラー伝播」を確認できる状態にしました。

## 変更内容
- `go/livestream_tag_cache_test.go:3`  
  - 追加テストに必要な import（`context`, `errors`, `regexp`, `sqlmock`, `sqlx`）を追加。
- `go/livestream_tag_cache_test.go:103`  
  - `TestGetLivestreamTagsMap_CacheMissThenHit` を追加。  
  - 1回目呼び出しで `livestream_tags` と `tags` のSQLを実行し、2回目呼び出しで追加SQLが発生しないことを `sqlmock` 期待値で検証。
- `go/livestream_tag_cache_test.go:156`  
  - `TestGetLivestreamTagsMap_DBError` を追加。  
  - `livestream_tags` 取得失敗時に `getLivestreamTagsMap` がエラーを返すことを検証。

## テスト結果
- 実行コマンド:
  - `GOPATH=/tmp/gopath GOMODCACHE=/tmp/gopath/pkg/mod GOCACHE=/tmp/go-build-cache GOTOOLCHAIN=auto go test ./...`
- 結果:
  - `ok   github.com/isucon/isucon13/webapp/go  0.253s`

## 証拠
- レビュー指摘確認:
  - `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/reports/06-qa-review.md`  
  - `QAREV-001` で要求されていた2点（ミス→ヒットの統合検証、DBエラー伝播）を実装。
- 実装要件確認:
  - `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/task/order.md`
- 変更内容確認:
  - `go/livestream_tag_cache_test.go:103`
  - `go/livestream_tag_cache_test.go:156`
- 対象実装確認（テスト対象）:
  - `go/livestream_tag_cache.go:86`