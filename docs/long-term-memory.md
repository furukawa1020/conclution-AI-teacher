# 長期会話memory境界

Issue #167は、親Issue #165を小さいPRへ分割した最初の基盤です。この段階では音声turnから長期memoryを読み書きしないため、ゲストと通常音声のlatencyは変わりません。

`GET /api/v1/conversation-memory`、`PUT`、`DELETE`は、App Checkと5分以内のパスキー再認証を通ったprincipalだけが利用できます。UIDをbody、query、pathから受け取らず、認証済みprincipalからだけ導出します。guest、期限切れpasskey、別providerはFirestore操作の前に同じ固定problem codeで拒否します。既定値はOFFです。

Firestoreの`conversation_memory_settings_v1`は、HMAC-SHA-256で不可逆化したUID document key、同意boolean、正のgeneration、schema、更新時刻だけを持ちます。`conversation_memories_v1`は同じdocument key、generation、AES-256-GCM暗号文、nonce、30日TTLだけを持ちます。raw UID、caption、credential ID、userHandleを平文保存しません。暗号AADはpurpose、schema、HMAC化UID、generationへ束縛します。鍵は既存のSecret Manager注入鍵から用途分離して使い、Cloud Runは復号可能なのでE2EEではありません。

保存可能なpayloadは、各4件・各96 rune以内の`topics`、`preferences`、`openLoops`だけです。空payload、未知schema、oversize、高確度PII、期限外、foreign UID、異なるgenerationは拒否します。opt-outと削除はsettingのgenerationを進めてmemory documentを同じtransactionで削除するため、以前のgenerationを持つ遅延writeは復活できません。

次のPRで、会話終了後の非同期writeを追加します。その後、次回セッション開始時だけの単回readとUIを追加します。各turnでFirestoreを読まず、guestはこの経路へ接続しません。
