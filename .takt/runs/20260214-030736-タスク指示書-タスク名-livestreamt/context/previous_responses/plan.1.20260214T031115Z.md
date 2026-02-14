要求に沿って、まずタスク指示書の参照資料を特定するために `.takt/.../context/task` 配下と既存レポートを読み、ソース・オブ・トゥルースを確定してから実装方針を組み立てます。まず対象ファイル一覧を取得します。
`rg` が使えない環境だったので、`find` に切り替えて参照資料とレポートの実体を列挙します。
参照資料として見つかったのは `order.md` のみで、レポートディレクトリには既存ファイルがありません。次に `order.md` 本文を読み、そこで指定される「参照資料」を最優先で追跡します。
`order.md` の参照資料定義に従い、次は実コードから `livestreamTags` 関連のハンドラ・ユースケース・リポジトリ・モデル・テストを特定します。ここで影響範囲を確定します。
ファイル名では SQL しか出なかったため、次にコード本文を全文検索して `livestream_tags` / `livestreamTags` の実装箇所を抽出します。
検索結果から実装の中心は `go/livestream_handler.go` と周辺モデルにあると判明しました。次に対象 Go ファイル群を読んで、取得・更新の具体的な呼び出し経路を確定します。
関連コードの実装位置は確定できました。続いて、ムーブメントで参照必須と指定されている Knowledge の元ファイルを確認し、設計判断の制約を明文化します。
`livestream_tags` への書き込みは現状 `reserveLivestreamHandler` の INSERT のみで、更新APIは実質この作成処理です。続けて `TagModel` と既存キャッシュ実装（あれば）を調べ、実装方式を揃える方針を決めます。
既存のオンメモリキャッシュ実装は `user_handler.go` にあるため、同じロック方針・初期化方針を流用できるか確認します。
Open Question（整合性の適用範囲）を確定するために、デプロイ構成が単一プロセスかを `README` と起動構成から確認します。
**参照資料の確認結果**
- 参照必須資料 ` .takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/task/order.md` を読了。
- `Report Directory`（`.takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/reports/`）には既存レポートが存在せず、replan 前提の履歴差分はなし。
- Knowledge 元ファイル ` .takt/runs/20260214-030736-タスク指示書-タスク名-livestreamt/context/knowledge/plan.1.20260214T030810Z.md` を確認。

**現状調査（ファクト）**
- `livestream_tags` 読み取り箇所  
  - `go/livestream_handler.go:191`（tag検索時の `livestream_tags` 直読）  
  - `go/livestream_handler.go:535`（配信レスポンス生成時の `livestream_tags` 直読）
- `livestream_tags` 更新箇所  
  - `go/livestream_handler.go:153`（予約作成時 INSERT）
- `fillLivestreamsResponse` が複数 API の共通経路  
  - `go/livestream_handler.go:229` `:268` `:313` `:427` から呼ばれる。
- 既存テストは `go` 配下に未存在（`*_test.go` なし）。
- 既存オンメモリキャッシュ実装例は `go/user_handler.go:38` 以降（`sync.RWMutex` + map）。

**設計判断**
- 小規模タスクではない（複数ファイル変更 + TTL/整合性の設計判断が必要）。
- キャッシュは `livestream_id -> []Tag` を主キーとする専用モジュールを新設。
- TTL は `60s` 固定。
- 整合性保証範囲は「単一プロセス内」に限定。  
  - 根拠: 要件がオンメモリ指定で、リポジトリ内に分散同期機構なし。  
  - 複数プロセス間の同期待ちは設計上不可（このタスク範囲外）。

**実装方針（implement への指示）**
1. `go/livestream_tag_cache.go` を新規作成。  
   - `sync.RWMutex` 保護の map。  
   - 値は `tags` と `expireAt`。  
   - API: `Get`, `Set`, `Invalidate`, `GetMany`（または miss 回収ヘルパ）。
2. `go/livestream_handler.go` の `fillLivestreamsResponse` 系をリファクタ。  
   - `livestream_tags` 取得はまずキャッシュ参照。  
   - miss のみ DB 取得し、取得結果を cache に `Set`。  
   - 空タグもキャッシュ（空配列）して miss 連打を防止。
3. 更新反映（整合性）を `reserveLivestreamHandler` に追加。  
   - DB更新成功（commit 成功）後に対象 `livestream_id` のキャッシュを `Set` もしくは `Invalidate`。  
   - commit 前に cache 永続更新しない（ロールバック時の不整合回避）。
4. `searchLivestreamsHandler` の `livestream_tags` 直読経路（`go/livestream_handler.go:191`）は、同タスクの「参照処理」対象としてキャッシュ化する。  
   - 必要なら tag 検索用のキー設計（`tag_id -> livestreamIDs`）を同モジュール内に追加。  
   - 追加時は更新時（reserve）に対応キーも同期/無効化。
5. 初期化配線  
   - グローバル初期化（`var` 初期化）で十分。`main.go` 変更は最小化。

**テスト方針**
- 新規: `go/livestream_tag_cache_test.go`
1. TTL テスト: set→即get(hit)→60秒相当経過後 miss（テストでは短TTL注入）。
2. 更新反映テスト: 同一キーを再 `Set` して最新値が返る。
3. 同時実行テスト: goroutine で get/set/invalidate を並行実行し panic/data race を起こさない。
- 既存実行: `go test ./...` を実行して全体回帰確認。

**影響範囲**
- 変更: `go/livestream_handler.go`
- 新規: `go/livestream_tag_cache.go`
- 新規: `go/livestream_tag_cache_test.go`
- （必要時のみ）変更: `go/main.go`（初期化配線を追加する場合）