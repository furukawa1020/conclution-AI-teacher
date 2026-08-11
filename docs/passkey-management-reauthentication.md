# パスキー管理の再認証境界

credential の追加は、匿名アカウントの初回登録とは別の ceremony として扱う。初回登録用
`BeginRegistration` は新しい UID と userHandle を生成するため、既存アカウントの管理には
利用しない。

## 認可境界

管理 route は voice の機能フラグとは独立して常に次を要求する。

- Firebase ID token と App Check token の組をサーバーで検証できる
- verified principal が `custom` / `passkey-v1` / account verified である
- principal の `PasskeyAt` が現在時刻から5分以内で、許容する未来ずれは30秒以内である
- UID と AppID は verified principal からのみ取得し、path・query・body から受け取らない

失敗理由は外部へ区別せず、`passkey_management_reauthentication_required` を返す。通常の
Firebase セッションや新しい ID token の `AuthTime` だけでは、この境界を越えられない。

## 追加 credential ceremony

`POST /api/v1/passkeys/credentials/registration:begin` は既存ユーザーを UID で読み、保存済みの
同じ userHandle に対する WebAuthn registration options を作る。WebAuthn の `user.name` には
Firebase UID を使わず、固定の仮名を使う。既存 credential ID は options に含めず、重複と
上限は Store の原子的な `CreateCredential` で拒否する。

サーバー側 ceremony は次の組へ束縛する。

- purpose: `credential-addition-v1`
- AppID の SHA-256
- principal UID の SHA-256（raw UID は ceremony 文書に保存しない）
- 既存アカウントの userHandle
- 5分の有効期限

`POST /api/v1/passkeys/credentials/registration:finish?ceremonyId=...` は ceremony を先に一回消費し、
同じ principal/AppID/userHandle と user verification を確認してから credential を追加する。
principal 差し替え、AppID 差し替え、期限切れ、replay、8件上限、同時競合はすべて
fail-closed とする。成功応答は本文なしの `204` で、新しい Firebase token や UID、raw
credential ID を返さない。追加登録の失敗は `passkey_credential_registration_failed` に固定する。

回復、account 削除、credential 一覧・失効 UI はこの境界の対象外であり、親 Issue #19 の
別トランシェで扱う。
