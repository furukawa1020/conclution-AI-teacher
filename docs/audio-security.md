# 音声セキュリティ設計

## 結論

「音声を一時データにして機能を削る」のではなく、保存音声をクライアント暗号化し、復号経路を独立した鍵ブローカーへ隔離します。履歴、再生、後日再評価は維持します。

ただし、無人の後日再評価と「サーバーが絶対に復号できないE2EE」は同時には成立しません。そのため、製品上も次の二つを混同しません。

| モード | 履歴・再生 | 無人の後日再評価 | 復号条件 |
|---|---:|---:|---|
| 管理型セキュア履歴 | 可能 | 可能 | 認可済みworkerが一回限り復号 |
| Vault | 可能 | 不可 | 本人が端末で鍵を解除した時だけ |

どちらもVertex AIの処理中まで平文を見せない方式ではありません。Live意味処理を行う時間だけ、平文音声が認可済みLive GatewayとVertex AIのメモリ上に存在します。

## 録音暗号

- 録音の入力音声とAI応答音声を別トラックにする
- トラックごとに新しい256-bit DEKを生成する
- 固定サイズのチャンクをAES-256-GCMで暗号化する
- 同じDEKでnonceを再利用しない
- AADへschema version、所有者binding、録音ID、方向、codec、sample rate、channels、chunk indexを含める
- manifestへ総チャンク数、総暗号文サイズ、連鎖SHA-256を保存する
- 欠落、重複、並べ替え、別ユーザー・別録音への差し替えを拒否する
- オブジェクト名はランダム128-bit IDとし、氏名、UID、題名を含めない

この中核は `crates/audio_vault` にあり、秘密鍵のDebug表示を常に伏せ、復号平文をdrop時にzeroizeします。

## 鍵階層

公開ベータ以降は、ユーザーごとのCloud KMS RSA-OAEP-3072/SHA-256鍵で録音DEKを包みます。

```text
ユーザーKMS RSA鍵
└─ 公開鍵: ブラウザが録音DEKをwrap
└─ 秘密鍵: KMS外へ出ない
     └─ Key Brokerだけが利用
          └─ トラックDEK
               └─ AES-GCM暗号化音声チャンク
```

再生時、ブラウザは一時鍵ペアを生成します。Key Brokerは認可後にDEKを開き、その一時公開鍵へ再wrapします。再生サーバーへ平文音声を返しません。

管理型セキュア履歴の後日再評価では、Cloud Tasksの特定ジョブ、特定録音generation、短い期限へ束縛した許可を一回だけ消費します。workerは音声をメモリ上でだけ復号し、KMSを直接呼ばずKey Brokerを経由します。

## IAM分離

| サービスID | 許可 | 明示的に持たせないもの |
|---|---|---|
| API | Vertex、Firestore | 音声Storage、KMS |
| Live Gateway | Vertex Live | Storage、KMS |
| Cipher Gateway | 指定暗号文のread/write | KMS decrypt、Vertex |
| Key Broker | 録音DEK用KMS操作 | Storage、Vertex |
| Re-eval Worker | 指定object read、Vertex、Broker invoke | KMS直接操作、bucket list |
| Key Lifecycle | KMS version無効化・破棄 | decrypt、Storage |

サービスアカウントJSON鍵は作りません。Cloud Runへ割り当てたサービスIDのApplication Default Credentialsを使います。

## Live音声

```text
Firebase Hosting                 voice.<domain>
Rust/Wasm UI ── ticket POST ──→ HTTPS LB / Cloud Armor
      │                              │
      └──────── WSS + cookie ───────→ Live Gateway ──→ Vertex Live
```

ブラウザWebSocketは任意のAuthorizationヘッダーを付けられないため、先にHTTPS POSTでFirebase ID token、App Check、Originを検証します。発行するticketは30〜60秒、単発、UID・Origin・session ID束縛とし、`Secure; HttpOnly; SameSite=Strict` cookieで渡してWSS handshake時に原子的に消費します。

## 保存と削除

保持期間は一律にせず、raw audio、AI応答音声、文字起こし、評価結果ごとに選択できます。

- 30日
- 90日
- 1年
- 自分で削除するまで
- 録音単位で固定

アカウント削除時は、将来の再生・再評価を即時停止し、ユーザー鍵を破棄予定状態へ移し、音声、wrapped DEK、manifest、文字起こし、評価、通知tokenを削除します。「アプリから即時アクセス不能」と、Storage soft deleteやKMS破棄待機を含む「物理削除完了」は別の日時として表示します。

## 同意

次を一括同意にしません。

- マイク利用
- raw audio保存
- Vertex AI処理
- AI応答音声保存
- 後日再評価
- 教師・組織との共有
- 品質改善・学習利用
- 話者識別、声紋、感情推定

品質改善・学習利用は既定OFFです。声を認証要素として使わず、声だけで課金、成績、共有、削除を承認しません。

## ログ禁止項目

音声、文字起こし、prompt/response、Firebase token、App Check token、cookie、署名URL、DEK、session handleはログへ出しません。ログにはrequest ID、UIDの短い不可逆hash、処理時間、論理モデルID、エラー分類だけを残します。

## 最低限の侵入試験

- 他人の録音IDで取得できない
- Live ticketを二度使えない
- チャンクの改ざん、欠落、並べ替えを検出する
- Cipher GatewayからKMSを利用できない
- Key BrokerからStorageを閲覧できない
- 鍵破棄後は暗号文が残っていても復号できない
- ログとブラウザキャッシュへ音声・tokenが残らない

参考:

- [Cloud KMS envelope encryption](https://cloud.google.com/kms/docs/envelope-encryption)
- [Cloud KMS separation of duties](https://cloud.google.com/kms/docs/separation-of-duties)
- [Cloud Run service identity](https://cloud.google.com/run/docs/securing/service-identity)
- [Cloud Storage client-side encryption](https://cloud.google.com/storage/docs/encryption/client-side-keys)
- [Firebase App Check custom backend](https://firebase.google.com/docs/app-check/custom-resource-backend)
