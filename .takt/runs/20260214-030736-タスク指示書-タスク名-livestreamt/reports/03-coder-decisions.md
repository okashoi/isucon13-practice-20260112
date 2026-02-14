今回の実装では、`livestreamTags` キャッシュ導入に関して以下を決定し反映しました。

1. `searchLivestreamsHandler` のタグ検索経路は `livestream_tags` の直読をやめ、キャッシュ経由のタグ判定へ統一しました。  
背景は、取得処理の両経路を同じキャッシュ制御に揃える必要があったためです。これにより TTL と整合性制御を `getLivestreamTagsMap` に集約できる構成になりました。

2. 更新時の整合性は「DBコミット成功後にキャッシュ invalidate」で担保しました。  
背景は、予約作成時に `livestream_tags` を更新した直後に古いキャッシュを返さない保証が必要だったためです。`reserveLivestreamHandler` ではコミット後に `livestreamTagsCache.invalidate(livestreamID)` を実行する方式を採用しました。