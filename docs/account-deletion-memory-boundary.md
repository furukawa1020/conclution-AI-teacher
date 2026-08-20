# アカウント削除と長期メモリの完全削除境界

Issue #165 の長期メモリを有効にした利用者が仮名アカウントを削除する場合、
削除処理は次の順序をサーバーが所有する。

1. 長期メモリの同意generationを進めて無効化し、暗号化recordを削除する。
2. パスキーcredential、user handle、ceremonyを削除済み状態へ移す。
3. Firebase Authの仮名アカウントを削除する。

最初の長期メモリtombstoneに失敗した場合、後続のパスキー・Firebase削除へ
進まない。これにより、会話応答後の非同期writeが遅れて到着しても旧generation
では保存できず、削除済みメモリを復活させない。Firebase削除だけが一時失敗した
場合は、同じ処理を再実行できる。各段階はraw UID、credential、会話本文を
responseや監査ログへ追加しない。

この境界は明示的なアカウント削除操作だけで実行する。通常の音声turnや会話開始
には追加のFirestore read/writeを入れず、first-audible latencyへ影響させない。
