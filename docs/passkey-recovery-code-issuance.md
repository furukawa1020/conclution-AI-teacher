# 単回回復コードの事前発行境界

Issue #94 の第一段階では、5分以内のパスキー管理再認証を通った仮名アカウント
だけが回復コードを事前発行できる。コードは256-bitの乱数をcanonical base64urlで
表した `krc1_` prefix付きの値で、HTTP responseへ一度だけ返す。期限は30日である。

Firestoreへraw codeは保存しない。purpose分離したSHA-256 digest、不可逆account key、
発行時刻、期限だけを、account側documentとcode indexへ同一transactionで保存する。
再発行時は旧indexの完全な関係を検証してから削除し、新しいdigestへ置き換える。
別accountとのdigest衝突、欠損index、壊れた時刻・schema、削除済みaccountは
fail-closedにする。

アカウント削除transactionは有効な回復account documentとcode indexを両方読み、
相互参照が一致する場合だけパスキーcredentialと一緒に削除する。遅延callbackや
再発行によって削除済みコードを復活させない。両collectionの `expiresAt` は
Firestore TTL必須policyへ追加する。

発行routeは既存の
App Check、verified principal、5分以内のpasskey proof、二段rate limitをそのまま
要求する。raw code、raw UID、credential ID、音声、文字起こしをlog・監査eventへ
出さない。

回復begin routeはApp Checkと二段rate limitを通した後、受け取ったcanonical codeを
直ちにpurpose分離digestへ変換する。Firestore transactionで未削除account、期限内の
account/code相互参照、既存credential index全体を検証し、同じopaque userHandleの
WebAuthn registrationを開始する。5分ceremonyにはApp ID digest、UID digest、code
digest、userHandle、sessionだけを束縛し、raw codeとraw UIDを保存・応答しない。

beginはコードを消費しない。WebAuthn検証済みcredentialの追加とコード消費を同じ
transactionにするfinish境界が公開されるまでは、ブラウザUIから回復フローを開始しない。
