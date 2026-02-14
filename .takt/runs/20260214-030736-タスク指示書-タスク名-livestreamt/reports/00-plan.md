# タスク計画

## 元の要求
Implement using only the files in `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/task`.
Primary spec: `.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/task/order.md`.
Use report files in Report Directory as primary execution history.
Do not rely on previous response or conversation summary.

## 分析結果

### 目的
`livestreamTags` の参照・更新処理にオンメモリキャッシュを導入し、TTL 60秒を厳密に適用しつつ、更新直後の整合性を崩さない実装・テスト方針を確定する。

### スコープ
- `livestream_tags` 読み取り経路  
  - `searchLivestreamsHandler` 内のタグ検索経路
  - `fillLivestreamsResponse` 内の配信詳細組み立て経路
- `livestream_tags` 更新経路  
  - `reserveLivestreamHandler` 内の `livestream_tags` INSERT 後処理
- 追加対象  
  - `livestreamTags` 専用オンメモリキャッシュモジュール
  - キャッシュの単体テスト（TTL/更新反映/並行アクセス）

### 設計判断

#### ファイル構成
| ファイル | 役割 |
|---------|------|
| `go/livestream_handler.go` | 既存の `livestream_tags` 読み書き経路をキャッシュ連携に変更 |
| `go/livestream_tag_cache.go` | `livestream_id` 単位のタグキャッシュ（get/set/invalidate, expireAt, lock）を実装 |
| `go/livestream_tag_cache_test.go` | TTL・更新反映・同時実行整合性を検証する単体テスト |

#### 設計パターン
- `sync.RWMutex + map` のインメモリキャッシュ（既存 `user_handler.go` のキャッシュ実装方針に合わせる）
- Cache-Aside パターン（参照時: cache miss のみ DB 読み取り、更新時: DB 成功後に cache 同期/無効化）
- 単一プロセス整合性保証（オンメモリ要件に準拠、プロセス間同期は対象外）

### 実装アプローチ
1. `livestreamTags` キャッシュモジュールを新規作成し、エントリに `tags` と `expireAt` を持たせる（TTL=60秒固定）。
2. `fillLivestreamsResponse` の `livestream_tags` 取得をキャッシュ優先に変更し、miss 分のみ DB 取得してキャッシュ保存する。
3. `searchLivestreamsHandler` のタグ検索経路でも `livestream_tags` 参照をキャッシュ経由化する。
4. `reserveLivestreamHandler` で DB 更新成功後（コミット成功後）に対象配信のキャッシュを更新または無効化し、更新直後の古い返却を防ぐ。
5. 単体テストを追加し、`go test ./...` 実行を前提に検証する。

## 実装ガイドライン
- TTL はハードコードで `60 * time.Second` とし、定数化して参照箇所を統一する。
- キャッシュ更新は必ず DB の確定後に実施し、ロールバック時にキャッシュだけ進まないようにする。
- 空タグ結果もキャッシュし、同一キーの無駄な DB 再読込を防ぐ。
- キャッシュモジュール外から内部 map や lock を触らせない（公開 API を最小化）。
- エラー時の HTTP 返却は既存ハンドラ方針（`echo.NewHTTPError`）を維持する。
- 変更で未使用化したコードが発生した場合は同一タスク内で削除する。

## 確認事項
- Open Question の整合性範囲は、今回の要件・構成上「単一プロセス内保証」で確定する。複数プロセス間整合性は対象外。