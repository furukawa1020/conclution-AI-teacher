# DBレス短命session context

Issue #175 は、#171の長期memory capabilityを#173の原子的single-use境界で消費し、成功した一回だけ別purposeの短命session contextへ変換する。`POST /api/v1/conversation-memory/context:consume` はApp Check、verified principal、5分以内のpasskeyを要求し、bodyには有限capabilityだけを受ける。UIDとApp IDは認証済みprincipalからのみ導出する。

session contextは保存暗号・発行capability暗号とは別のHMAC-SHA-256 purpose-separated KDF鍵によるAES-256-GCMで暗号化する。AADはpurpose、HMAC化UID、HMAC化App IDへ束縛する。暗号文内はschema、同意generation、発行時刻、絶対15分期限、128-bit session ID、有限memory payloadだけで、raw UID、raw App ID、capability ID、caption、発話全文、AI応答を含めない。HTTP responseはopaque tokenと固定900秒だけを返し、logは有限outcomeだけを持つ。

乱数、session ID、JSON、上限サイズはFirestore消費前に確定する。消費transactionの成功後には失敗を返し得る処理を残さず、AES-GCM sealとbase64url化だけを行う。これにより準備失敗がcapabilityを失うことを防ぎ、100並行consumeでもsession tokenは一件だけ発行される。

後続のsession context検証はAEAD復号と有限field検証だけで、FirestoreやCloud Storageを読まない。このPRはvoice transportと会話agentへtokenをまだ渡さないため、既存turn latencyと回答内容を変えない。DBレス契約との交換条件として、発行後のopt-outは発行済みtokenを即時revokeせず、principalへ束縛されたtokenが最大15分で絶対失効する。発行前のopt-outとgeneration差替えはsingle-use transactionで拒否する。

## ブラウザでの先行準備

Issue #177 では、非ゲストの音声開始で5分以内のパスキー再認証が完了した直後に、`context:begin` と `context:consume` をマイク、AudioContext、Native provider準備と並行して開始する。このPromiseを音声開始経路はawaitしないため、Firestoreが遅い、長期メモリが無効、通信が失敗する、のいずれでもlisten開始時計は動かない。ゲストは両APIを呼ばない。

ブラウザは同じvoice session generationで最大一度だけ準備する。capabilityとsession contextは専用moduleのclosureだけに保持し、DOM、CustomEvent、console、Storage、URLへ公開しない。停止、世代差替え、`pagehide`、15分期限でAbortSignalを閉じて参照を破棄する。公開snapshotは `preparing`、`ready`、`unavailable`、`failed`、`cancelled`、`expired` の有限状態とgenerationだけである。ここではsession contextをvoice requestへまだ渡さない。

release preflightは実Cloud Runの両endpointへFirebase Hosting originからOPTIONSを送り、beginはAuthorizationとApp Check、consumeはそれらにContent-Typeを加えたheader集合だけがHTTP 204、Hosting origin、`cross-origin` resource policy、POST許可になることを検証する。
