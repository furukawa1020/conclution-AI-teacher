# 長期会話メモリの非同期保存境界

Issue #169 は、パスキーで本人確認された利用者が明示的に有効化した場合だけ、応答完了後に有限な semantic memory を保存する経路を追加する。

通常 HTTP、NDJSON stream、WebSocket live の全経路は、最終応答の書き込みに成功してから同じ非同期 dispatcher へ投入する。投入は bounded channel への non-blocking send だけであり、Firestore、追加推論、候補抽出の完了を待たない。キューが満杯なら保存を捨てて会話応答を優先する。worker 数、キュー容量、job TTL、処理 timeout は有限で、終了時には未処理 token を消去する。

ゲスト、別 provider、未確認アカウント、空の state token は dispatcher の手前で除外する。opt-out の場合は consent 確認後に候補を生成せず、memory write も行わない。保存失敗、期限切れ、generation 競合、候補不正は固定 enum の観測結果だけを残し、UID、state token、字幕、発話、モデル応答は log に残さない。

候補抽出は、認証済み短期 state の `ThoughtStateGraph` にすでに存在する `Goals`、`Claims`、`OpenLoops` だけを最大4件へ有限化する。`ConversationSummary`、Native caption、利用者の発話全文、AI応答、coach evidence は読まない。既存の暗号化・PII拒否・generation tombstone は保存直前にも再検証される。

この段階は次回セッションの read を音声 hot path へまだ接続しない。Native audio からの要約生成や、各 turn での Firestore read も追加しない。次の小さな PR で、開始時1回だけ取得する有限 read と、読み込み失敗時に会話を変えない境界を実装する。
