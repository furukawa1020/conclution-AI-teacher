# DBレス短命session context

Issue #175 は、#171の長期memory capabilityを#173の原子的single-use境界で消費し、成功した一回だけ別purposeの短命session contextへ変換する。`POST /api/v1/conversation-memory/context:consume` はApp Check、verified principal、5分以内のpasskeyを要求し、bodyには有限capabilityだけを受ける。UIDとApp IDは認証済みprincipalからのみ導出する。

session contextは保存暗号・発行capability暗号とは別のHMAC-SHA-256 purpose-separated KDF鍵によるAES-256-GCMで暗号化する。AADはpurpose、HMAC化UID、HMAC化App IDへ束縛する。暗号文内はschema、同意generation、発行時刻、絶対15分期限、128-bit session ID、有限memory payloadだけで、raw UID、raw App ID、capability ID、caption、発話全文、AI応答を含めない。HTTP responseはopaque tokenと固定900秒だけを返し、logは有限outcomeだけを持つ。

乱数、session ID、JSON、上限サイズはFirestore消費前に確定する。消費transactionの成功後には失敗を返し得る処理を残さず、AES-GCM sealとbase64url化だけを行う。これにより準備失敗がcapabilityを失うことを防ぎ、100並行consumeでもsession tokenは一件だけ発行される。

後続のsession context検証はAEAD復号と有限field検証だけで、FirestoreやCloud Storageを読まない。このPRはvoice transportと会話agentへtokenをまだ渡さないため、既存turn latencyと回答内容を変えない。DBレス契約との交換条件として、発行後のopt-outは発行済みtokenを即時revokeせず、principalへ束縛されたtokenが最大15分で絶対失効する。発行前のopt-outとgeneration差替えはsingle-use transactionで拒否する。
