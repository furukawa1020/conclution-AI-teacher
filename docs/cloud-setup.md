# Firebase / Google Cloud接続手順

## 現在の専用環境

- Google Cloud / Firebase project: `kotae-ai-u22-2026`
- 公開URL: `https://kotae-ai.web.app`
- Firebase Hosting: 静的Wasm UI、Passkey等の`/api/**` Cloud Run rewrite。音声stream / WebSocketは固定`run.app`へ認証付きで直接接続
- Firebase Auth: Passkey検証後に発行する仮名custom account session
- Firebase App Check: reCAPTCHA Enterprise、Passkey ceremonyと独自APIで強制
- Firestore: `(default)`、`asia-northeast1`、削除保護、ceremonyとrate counterのTTL
- Cloud Run: `kotae-api`、`asia-northeast1`
- Cloud Run実行ID: `kotae-api-runtime@kotae-ai-u22-2026.iam.gserviceaccount.com`
- Cloud Speech-to-Text V2: `asia-northeast1`、自然会話向け`long`単独、`ja-JP`
- Cloud Text-to-Speech: `asia-northeast1`、`ja-JP-Chirp3-HD-Kore`
- Vertex AI: `global`、高速`gemini-3.6-flash`、精密`gemini-3.1-pro-preview`
- Sensitive Data Protection: `asia-northeast1`、厳格音声モードの文字起こし・応答検査
- Secret Manager: `kotae-conversation-state`

FirebaseとGoogle Cloudは、別々のプロジェクトをURLやAPI keyで接続する構成ではありません。FirebaseプロジェクトはFirebase機能を追加したGoogle Cloudプロジェクトそのものです。同じproject IDのHosting、Auth、App Check、Firestore、Cloud Run、Speech-to-Text、Text-to-Speech、Vertex AI、Secret Managerを使います。

ブラウザが使うFirebase Web API keyは公開識別子であり、Cloud RunやVertex AIの権限を与える秘密鍵ではありません。保護境界はFirebase ID token、App Check、Origin検証、IAM、Secret Managerです。

## リージョン境界

| 処理 | ロケーション | 入るデータ |
|---|---|---|
| Hosting / API | Hosting / Cloud Run `asia-northeast1` | 静的asset、voice request |
| Firestore | `asia-northeast1` | TTL付き評価メタデータ・rate counter・Passkeyのpublic credentialと短命ceremony |
| Speech-to-Text | `asia-northeast1` regional endpoint | raw audio |
| Sensitive Data Protection | `asia-northeast1` regional endpoint | 厳格モードの文字起こしと応答文 |
| Vertex AI | `global` | 文字起こし、標準モードの短い状態要約と今回添付したPDF |
| Text-to-Speech | `asia-northeast1` regional endpoint | 選ばれた短い応答文 |
| Crossref | Google Cloud外の公開REST API | intentional turnで明示し、tool-policyとPII screenを通過したDOIまたは最小topicだけ |

raw audioをVertex AIへ直接送るVertex Live APIは現在使いません。ただし文字起こしとPDFは`global`のVertex AIへ送られ、明示した研究queryはCrossrefへ送られるため、「すべての会話データが日本国内だけで処理される」とは説明しません。

## 必要なAPI

新しい専用projectを再現する場合は、対象projectを明示して必要APIを有効化します。

```powershell
$ProjectId = "kotae-ai-u22-2026"

gcloud services enable `
  run.googleapis.com `
  artifactregistry.googleapis.com `
  cloudbuild.googleapis.com `
  aiplatform.googleapis.com `
  dlp.googleapis.com `
  speech.googleapis.com `
  texttospeech.googleapis.com `
  firestore.googleapis.com `
  identitytoolkit.googleapis.com `
  firebaseappcheck.googleapis.com `
  recaptchaenterprise.googleapis.com `
  secretmanager.googleapis.com `
  logging.googleapis.com `
  monitoring.googleapis.com `
  serviceusage.googleapis.com `
  iamcredentials.googleapis.com `
  --project=$ProjectId
```

Cloud KMS、Cloud Storage、Cloud Tasksは、現在の保存しない音声経路には不要です。将来の保存音声Vaultや後日再評価を実装する時に、同意とIAM分離を含めて別途有効化します。

## Cloud Run実行IDとIAM

サービスアカウントJSON鍵は作りません。Cloud Runへ割り当てた専用サービスIDのApplication Default Credentialsを使います。

```powershell
$ProjectId = "kotae-ai-u22-2026"
$RuntimeSa = "kotae-api-runtime@$ProjectId.iam.gserviceaccount.com"

gcloud iam service-accounts create kotae-api-runtime `
  --display-name="KOTAE API runtime" `
  --project=$ProjectId
```

現在のruntimeに必要なproject-level roleは次です。

```powershell
$Roles = @(
  "roles/aiplatform.user",
  "roles/datastore.user",
  "roles/dlp.user",
  "roles/firebaseauth.viewer",
  "roles/speech.client",
  "roles/serviceusage.serviceUsageConsumer"
)

foreach ($Role in $Roles) {
  gcloud projects add-iam-policy-binding $ProjectId `
    --member="serviceAccount:$RuntimeSa" `
    --role=$Role
}

# Firebase custom tokenをJSON鍵なしで署名するため、runtime自身だけに
# serviceAccountTokenCreatorを付ける。project全体のservice accountへ広げない。
gcloud iam service-accounts add-iam-policy-binding $RuntimeSa `
  --member="serviceAccount:$RuntimeSa" `
  --role="roles/iam.serviceAccountTokenCreator" `
  --project=$ProjectId
```

音声やPDF用のStorage bucketはなく、runtimeへStorage roleやKMS roleを付けません。

## 会話状態鍵

`KOTAE_STATE_KEY_BASE64`は、暗号学的乱数32 byteをstandard Base64にした値です。リポジトリ、`.env`、shell history、Cloud Runの平文環境変数へ直接残しません。Secret Managerの`kotae-conversation-state`へ保存します。

```powershell
$ProjectId = "kotae-ai-u22-2026"
$RuntimeSa = "kotae-api-runtime@$ProjectId.iam.gserviceaccount.com"
$StateSecret = "kotae-conversation-state"

gcloud secrets create $StateSecret `
  --replication-policy=automatic `
  --project=$ProjectId

gcloud secrets add-iam-policy-binding $StateSecret `
  --member="serviceAccount:$RuntimeSa" `
  --role="roles/secretmanager.secretAccessor" `
  --project=$ProjectId
```

秘密値のversion追加は、安全な端末で生成した32 byteだけを入力ファイル経由で行い、標準出力へ表示しません。Secretへの`secretAccessor`はproject全体ではなく、このSecret単位でruntimeだけに付与します。

鍵をrotateすると、旧鍵で発行済みの15分tokenは復号できなくなります。短時間の会話状態を失効させてよいタイミングでrotateします。

## Cloud Run設定

本番で必要な主な値は次です。

```text
KOTAE_ENV=production
KOTAE_ALLOW_INSECURE_DEV=false
GOOGLE_CLOUD_PROJECT=kotae-ai-u22-2026
GOOGLE_CLOUD_LOCATION=global
KOTAE_ALLOWED_APP_IDS=<Firebase Web App ID>
KOTAE_FAST_MODEL=vertexai/gemini-3.6-flash
KOTAE_PRECISION_MODEL=vertexai/gemini-3.1-pro-preview
KOTAE_VERTEX_PRIORITY=false
KOTAE_COACH_RESTATEMENT_BINDING=true
KOTAE_SPEECH_LOCATION=asia-northeast1
KOTAE_SPEECH_MODEL=long
KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore
KOTAE_REQUEST_TIMEOUT=25s
KOTAE_VOICE_TIMEOUT=50s
KOTAE_MAX_REQUEST_BYTES=32768
KOTAE_MAX_VOICE_BYTES=13631488
KOTAE_VOICE_RATE_LIMIT_PER_MINUTE=12
KOTAE_VOICE_RATE_LIMIT_PER_DAY=120
KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE=20
KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY=200
KOTAE_PASSKEY_RP_ID=kotae-ai.web.app
KOTAE_PASSKEY_ORIGIN=https://kotae-ai.web.app
KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE=10
KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY=100
KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE=300
KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY=20000
KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true
KOTAE_STATE_KEY_BASE64=<Secret Managerから注入>
```

`KOTAE_SPEECH_LOCATION`は実装側でも`asia-northeast1`以外を拒否します。Vertexの`global`とSpeechの東京リージョンを同じ設定値で兼用しません。

既存のAuth / App Check設定を保ったまま更新する例:

```powershell
$ProjectId = "kotae-ai-u22-2026"
$RuntimeSa = "kotae-api-runtime@$ProjectId.iam.gserviceaccount.com"
$WebAppId = "<Firebase Web App ID>"

gcloud run deploy kotae-api `
  --source=. `
  --project=$ProjectId `
  --region=asia-northeast1 `
  --ingress=all `
  --allow-unauthenticated `
  --service-account=$RuntimeSa `
  --cpu=1 `
  --memory=1Gi `
  --concurrency=4 `
  --min-instances=1 `
  --max-instances=3 `
  --timeout=360 `
  --remove-env-vars="KOTAE_SPEECH_FALLBACK_MODEL,KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE,KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY" `
  --update-env-vars="KOTAE_ENV=production,KOTAE_ALLOW_INSECURE_DEV=false,GOOGLE_CLOUD_PROJECT=$ProjectId,GOOGLE_CLOUD_LOCATION=global,KOTAE_ALLOWED_APP_IDS=$WebAppId,KOTAE_FAST_MODEL=vertexai/gemini-3.6-flash,KOTAE_PRECISION_MODEL=vertexai/gemini-3.1-pro-preview,KOTAE_VERTEX_PRIORITY=false,KOTAE_COACH_RESTATEMENT_BINDING=true,KOTAE_SPEECH_LOCATION=asia-northeast1,KOTAE_SPEECH_MODEL=long,KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore,KOTAE_VOICE_TIMEOUT=50s,KOTAE_MAX_VOICE_BYTES=13631488,KOTAE_VOICE_RATE_LIMIT_PER_MINUTE=12,KOTAE_VOICE_RATE_LIMIT_PER_DAY=120,KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE=20,KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY=200,KOTAE_PASSKEY_RP_ID=kotae-ai.web.app,KOTAE_PASSKEY_ORIGIN=https://kotae-ai.web.app,KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE=10,KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY=100,KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE=300,KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY=20000,KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true" `
  --update-secrets="KOTAE_STATE_KEY_BASE64=kotae-conversation-state:latest"
```

`--set-env-vars`や`--set-secrets`は既存設定を消す可能性があるため、再配備では現在値を確認して`--update-*`を使います。Cloud Runのrequest timeoutは`360`秒にし、live / HTTPのturn全体に設けた6分の外側境界と一致させます。クライアントcaptureは最大3分30秒、サーバーcaptureは最大4分または20 ms PCM 12,000 frameで停止し、残りを認証・commit後の処理と安全終了に使います。`KOTAE_VOICE_TIMEOUT=50s`は別の内側処理境界です。音声、PDF、複数回のモデル呼び出しが同時にメモリへ載るため、既定の高いconcurrencyへ任せず、1 instanceあたり4 request、最大3 instanceへ明示的に制限します。

本番では`KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true`を維持し、未指定でもsecure defaultとして`true`になります。frontendを先に配備してPasskey UIを公開し、その後backendをこの設定で配備します。音声APIのbuffered、streaming、WebSocketすべてが、Passkey由来claimと5分以内の署名検証時刻`kotae_passkey_at`を要求します。Firebaseの`auth_time`はcustom token交換時に新しくなり得るため、freshness根拠には使いません。`false`は認証を迂回できるため、明示的なローカル開発以外では使いません。

`KOTAE_COACH_RESTATEMENT_BINDING`は状態tokenのrolling migration用flagです。新fieldを読めるrevisionをまず`false`で100% trafficへ移し、そのrevisionが安定してから同じcodeを`true`にした次revisionへ切り替えます。`true`のrevisionが発行したtokenを旧codeはstrict JSON decodeで拒否するため、rollback先は直前の`false`互換revisionに固定します。`false`revisionも既存tagは検証しますが、新しいtagは発行しません。`true`revisionはtagなしの旧`awaiting_restatement` scopeを推論前に消し、その発話を通常会話として扱うため、tagなしscopeをtoken更新で延命できません。

上の長期運用例はmigration完了後の`true`です。このfieldを初めて追加する一回だけ、最初のcandidate deployでは同じ引数の`KOTAE_COACH_RESTATEMENT_BINDING=true`を`false`へ置き換えます。互換revisionへ100%切替・検証後、同じimageから`true` revisionを作り、candidate検証後に100%へ切り替えます。以後の通常deployは`true`を維持します。

互換revisionのcandidate tag URLは、0% trafficへ移した時点で外します。`true`revision側はそこから発行されたtagなしscopeも採点へ使いませんが、不要な別endpointを残さないためです。revision自体はtagなし・0% trafficで残し、rollback時だけservice rootのtrafficを明示的に戻します。

Firebase HostingのCloud Run rewriteは、Cloud Run IAM用のID tokenを付けない公開transportです。そのため`kotae-api`は`--ingress=all --allow-unauthenticated`を維持します。これはAPI認証を無効にする設定ではありません。`/api/**`はアプリ側でFirebase ID tokenとApp Check tokenの両方、許可App ID、厳密なOrigin、二段rate limitを検証します。`--no-allow-unauthenticated`または`--ingress=internal-and-cloud-load-balancing`へ変更すると、Hostingからコンテナへ届かず汎用404になります。

UID単位の枠に加え、許可App ID全体のvoice枠をbody decode前に消費します。仮名accountを作り直してもproject全体の費用上限を素通りできないための二段目です。Passkeyの4 ceremony APIは、client単位の10/分・100/日と、App ID全体の300/分・20,000/日のサーキットブレーカーを別collectionで消費します。旧`KOTAE_PASSKEY_APP_RATE_LIMIT_*`は意味が曖昧なため互換扱いせず、残っているrevisionは起動時に移行エラーになります。配備時に旧変数を削除して新しい4変数を同時に設定します。App Checkは不正利用を減らしますが、Web attestationや通常tokenがすべての濫用を防ぐ保証ではありません。Go Admin SDKではcustom backend向けlimited-use token消費が未対応のため、現在は再利用可能なtoken検証、二段rate limit、厳密なOrigin、Cloud Run上限、Google Cloud quotaと請求アラートを重ねます。

## FirestoreとTTL

ブラウザからFirestoreへ直接アクセスさせません。`firestore.rules`はdeny-allを維持し、Cloud RunのruntimeだけがAdmin SDKで次を扱います。

| collection | 内容 | TTL field |
|---|---|---|
| `evaluations` | 従来評価APIの本文を含まない評価メタデータ | `expiresAt`、30日 |
| `evaluationRateLimits` | 従来評価APIのrate counter | `expiresAt`、48時間 |
| `voiceRateLimits` | 音声APIのrate counter | `expiresAt`、48時間 |
| `passkeyClientRateLimits` | Passkeyの登録・認証それぞれのbegin/finish、計4 ceremony APIで共有するclient単位rate counter | `expiresAt`、48時間 |
| `passkeyAppRateLimits` | 計4 ceremony APIで共有するFirebase App単位サーキットブレーカー | `expiresAt`、48時間 |
| `passkey_ceremonies_v1` | App ID・purpose・challengeへ束縛した単回ceremony | `expiresAt`、5分 |
| `passkey_users_v1` / `passkey_handles_v1` / `passkey_credentials_v1` | 仮名UID、user handle、public credential、sign counter等。秘密鍵は含まない | TTLなし。削除・回復UIは未実装 |

```powershell
$ProjectId = "kotae-ai-u22-2026"

gcloud firestore fields ttls update expiresAt `
  --collection-group=evaluations `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls update expiresAt `
  --collection-group=evaluationRateLimits `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls update expiresAt `
  --collection-group=voiceRateLimits `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls update expiresAt `
  --collection-group=passkeyClientRateLimits `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls update expiresAt `
  --collection-group=passkeyAppRateLimits `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls update expiresAt `
  --collection-group=passkey_ceremonies_v1 `
  --database="(default)" `
  --enable-ttl `
  --project=$ProjectId

gcloud firestore fields ttls list `
  --database="(default)" `
  --project=$ProjectId `
  --format="table(name,ttlConfig.state)"
```

6 collectionすべてが`ACTIVE`になるまで配備完了としません。TTLは即時削除の仕組みではありません。期限後の物理削除には遅延があり得るため、ceremonyは読み出しtransactionで単回消費し、5分期限もAPIが独立検証します。会話状態tokenの15分期限も復号後に検証します。

## Hosting

Web buildとHosting deployはリポジトリのscriptを使います。

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
powershell -ExecutionPolicy Bypass -File scripts/deploy-hosting.ps1 `
  -ProjectId kotae-ai-u22-2026
```

Passkey、`/me`等の通常APIは同一Originの`https://kotae-ai.web.app/api/**`を使い、Firebase HostingがCloud Runへrewriteします。低遅延音声はHosting rewriteがWebSocketを中継しないため、固定した`https://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/turns:stream`と`wss://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/live`へ直接接続します。Cloud Run側はFirebase token、App Check、exact Hosting Origin、mode、quotaを検証します。Cloud RunではWebSocketも長時間HTTP requestとしてrequest timeoutの対象になるため、`360`秒は永続接続の保証ではありません。切断後の再接続は新しいrequestとして扱い、確定済み音声を自動再送しません。同期圧縮HTTP fallbackの音声は2 MiB上限なので、3分級の発話を処理できるとは保証しません。

## Consoleで確認する項目

1. Passkey登録・認証が公開originで成功し、cancel / unsupported時にマイクを取得しないこと
2. App CheckのWeb App ID、reCAPTCHA Enterprise site key、Passkey ceremonyと独自APIの強制
3. Cloud Run revisionのruntime service account、Passkey/DLP環境変数、Secret参照、360秒request timeout
4. Speech-to-Text / Text-to-Speech / Vertex AI / DLPのquotaと請求アラート
5. Firestoreの6つのTTL policyが`ACTIVE`
6. runtime自身以外へ不要な`serviceAccountTokenCreator`がなく、Secretへruntime以外の不要な`secretAccessor`がないこと
7. Cloud Loggingへ音声、文字起こし、prompt/response、PDF、token、WebAuthn responseが出ていないこと
8. `KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true`で全音声transportが古い・非Passkey tokenを拒否すること

参考:

- [既存Google CloudプロジェクトへFirebaseを追加](https://firebase.google.com/docs/projects/use-firebase-with-existing-cloud-project)
- [Firebase HostingとCloud Run](https://firebase.google.com/docs/hosting/cloud-run)
- [Firebase App Check custom backend](https://firebase.google.com/docs/app-check/custom-resource-backend)
- [Cloud RunでのWebSocket利用](https://docs.cloud.google.com/run/docs/triggering/websockets)
- [Cloud Runのrequest timeout設定](https://docs.cloud.google.com/run/docs/configuring/request-timeout)
- [Cloud Runのquotaと上限](https://docs.cloud.google.com/run/quotas)
- [Cloud Speech-to-Textのquotaと上限](https://docs.cloud.google.com/speech-to-text/docs/quotas)
- [Cloud Speech-to-Text regional endpoints](https://cloud.google.com/speech-to-text/v2/docs/endpoints)
- [Cloud Text-to-Speech endpoints](https://cloud.google.com/text-to-speech/docs/endpoints)
- [Cloud Run Secret Manager integration](https://cloud.google.com/run/docs/configuring/services/secrets)
- [Firestore TTL](https://cloud.google.com/firestore/docs/ttl)
