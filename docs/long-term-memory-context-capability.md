# 長期会話メモリのsession capability

Issue #171 は、各voice turnでFirestoreを読まずに済むよう、セッション開始前の専用API `POST /api/v1/conversation-memory/context:begin` を追加する。APIはApp Checkと5分以内のパスキー再認証を要求し、body、query、pathからUIDやApp IDを受け取らない。guest、別provider、未確認account、期限切れpasskeyはStoreへ到達しない。

有効な同意とmemory recordは、MemoryStoreでは同一lock、Firestoreでは同一transaction snapshotで一度だけ取得する。opt-outとの競合、generation差替え、欠落recordはcapabilityを発行しない。voice HTTP、NDJSON stream、WebSocket liveのhandlerはこのAPIを呼ばず、既存の応答latencyを変更しない。

memoryはブラウザへ平文で返さない。保存暗号とは別にHMAC-SHA-256のpurpose-separated KDFで導出したAES-256-GCM鍵を使い、標準96-bit nonceで暗号化する。AADはpurpose、HMAC化UID、HMAC化App IDへ束縛する。暗号文内はschema、同意generation、発行時刻、15分期限、将来のsingle-use tombstone用128-bit capability ID、有限memory payloadだけを持つ。raw UID、raw App ID、caption、発話全文、AI応答は含めない。

このPRは発行と内部検証契約までである。ブラウザのbackground prefetch、voiceへの受け渡し、single-use消費、replay tombstone、会話agentへの反映は次の小さいIssueへ分離する。したがって、この時点では長期memoryが回答内容を変更することはない。
