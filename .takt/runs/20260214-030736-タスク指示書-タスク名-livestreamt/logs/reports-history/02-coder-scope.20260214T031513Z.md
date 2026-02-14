# 変更スコープ宣言

## タスク
`livestreamTags` のオンメモリキャッシュ実装（更新反映・60秒TTL・整合性確保）

## 変更予定
| 種別 | ファイル |
|------|---------|
| 作成 | `go/livestream_tag_cache.go` |
| 作成 | `go/livestream_tag_cache_test.go` |
| 変更 | `go/livestream_handler.go` |
| 変更 | `go/main.go` |

## 推定規模
Medium

## 影響範囲
- `livestream_tags` の取得処理（キャッシュヒット/ミス制御）
- `livestream_tags` の更新処理（予約作成後の整合性反映）
- 配信一覧/詳細レスポンスでのタグ解決経路
- 初期化時のキャッシュクリア動作
