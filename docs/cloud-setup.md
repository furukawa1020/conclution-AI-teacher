# Firebase / Google Cloud接続手順

## 現在の専用環境

- Google Cloud / Firebase project: `kotae-ai-u22-2026`
- 公開URL: `https://kotae-ai.web.app`
- Firebase Hosting: 静的Wasm UIと`/api/**`のCloud Run rewrite
- Firebase Auth: 匿名認証、未使用匿名アカウントの自動削除
- Firebase App Check: reCAPTCHA Enterprise、Authenticationと独自APIで強制
- Firestore: `(default)`、`asia-northeast1`、削除保護、TTL
- Cloud Run: `kotae-api`、`asia-northeast1`
- Cloud Run実行ID: `kotae-api-runtime@kotae-ai-u22-2026.iam.gserviceaccount.com`
- Cloud Speech-to-Text V2: `asia-northeast1`、`chirp_3`、`ja-JP`
- Cloud Text-to-Speech: `asia-northeast1`、`ja-JP-Chirp3-HD-Kore`
- Vertex AI: `global`、高速`gemini-3.6-flash`、精密`gemini-3.1-pro-preview`
- Secret Manager: `kotae-conversation-state`

FirebaseとGoogle Cloudは、別々のプロジェクトをURLやAPI keyで接続する構成ではありません。FirebaseプロジェクトはFirebase機能を追加したGoogle Cloudプロジェクトそのものです。同じproject IDのHosting、Auth、App Check、Firestore、Cloud Run、Speech-to-Text、Text-to-Speech、Vertex AI、Secret Managerを使います。

ブラウザが使うFirebase Web API keyは公開識別子であり、Cloud RunやVertex AIの権限を与える秘密鍵ではありません。保護境界はFirebase ID token、App Check、Origin検証、IAM、Secret Managerです。

## リージョン境界

| 処理 | ロケーション | 入るデータ |
|---|---|---|
| Hosting / API | Hosting / Cloud Run `asia-northeast1` | 静的asset、voice request |
| Firestore | `asia-northeast1` | TTL付き評価メタデータとrate counter |
| Speech-to-Text | `asia-northeast1` regional endpoint | raw audio |
| Vertex AI | `global` | 文字起こし、短い状態要約、今回添付したPDF |
| Text-to-Speech | `asia-northeast1` regional endpoint | 選ばれた短い応答文 |

raw audioをVertex AIへ直接送るVertex Live APIは現在使いません。ただし文字起こしとPDFは`global`のVertex AIへ送られるため、「すべての会話データが日本国内だけで処理される」とは説明しません。

## 必要なAPI

新しい専用projectを再現する場合は、対象projectを明示して必要APIを有効化します。

```powershell
$ProjectId = "kotae-ai-u22-2026"

gcloud services enable `
  run.googleapis.com `
  artifactregistry.googleapis.com `
  cloudbuild.googleapis.com `
  aiplatform.googleapis.com `
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
  "roles/firebaseauth.viewer",
  "roles/speech.client",
  "roles/serviceusage.serviceUsageConsumer"
)

foreach ($Role in $Roles) {
  gcloud projects add-iam-policy-binding $ProjectId `
    --member="serviceAccount:$RuntimeSa" `
    --role=$Role
}
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
KOTAE_SPEECH_LOCATION=asia-northeast1
KOTAE_SPEECH_MODEL=chirp_3
KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore
KOTAE_REQUEST_TIMEOUT=25s
KOTAE_VOICE_TIMEOUT=50s
KOTAE_MAX_REQUEST_BYTES=32768
KOTAE_MAX_VOICE_BYTES=12582912
KOTAE_VOICE_RATE_LIMIT_PER_MINUTE=12
KOTAE_VOICE_RATE_LIMIT_PER_DAY=120
KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE=20
KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY=200
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
  --service-account=$RuntimeSa `
  --cpu=1 `
  --memory=1Gi `
  --concurrency=4 `
  --min-instances=0 `
  --max-instances=3 `
  --timeout=60 `
  --update-env-vars="KOTAE_ENV=production,KOTAE_ALLOW_INSECURE_DEV=false,GOOGLE_CLOUD_PROJECT=$ProjectId,GOOGLE_CLOUD_LOCATION=global,KOTAE_ALLOWED_APP_IDS=$WebAppId,KOTAE_FAST_MODEL=vertexai/gemini-3.6-flash,KOTAE_PRECISION_MODEL=vertexai/gemini-3.1-pro-preview,KOTAE_SPEECH_LOCATION=asia-northeast1,KOTAE_SPEECH_MODEL=chirp_3,KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore,KOTAE_VOICE_TIMEOUT=50s,KOTAE_MAX_VOICE_BYTES=12582912,KOTAE_VOICE_RATE_LIMIT_PER_MINUTE=12,KOTAE_VOICE_RATE_LIMIT_PER_DAY=120,KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE=20,KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY=200" `
  --update-secrets="KOTAE_STATE_KEY_BASE64=kotae-conversation-state:latest"
```

`--set-env-vars`や`--set-secrets`は既存設定を消す可能性があるため、再配備では現在値を確認して`--update-*`を使います。Cloud Runのtimeoutは、内部の50秒voice timeoutより長い60秒にします。音声、PDF、複数回のモデル呼び出しが同時にメモリへ載るため、既定の高いconcurrencyへ任せず、1 instanceあたり4 request、最大3 instanceへ明示的に制限します。

UID単位の枠に加え、許可App ID全体のvoice枠をbody decode前に消費します。匿名UIDを作り直してもproject全体の費用上限を素通りできないための二段目です。App Checkは不正利用を減らしますが、Web attestationや通常tokenがすべての濫用を防ぐ保証ではありません。Go Admin SDKではcustom backend向けlimited-use token消費が未対応のため、現在は再利用可能なtoken検証、二段rate limit、厳密なOrigin、Cloud Run上限、Google Cloud quotaと請求アラートを重ねます。

## FirestoreとTTL

ブラウザからFirestoreへ直接アクセスさせません。`firestore.rules`はdeny-allを維持し、Cloud RunのruntimeだけがAdmin SDKで次を扱います。

| collection | 内容 | TTL field |
|---|---|---|
| `evaluations` | 従来評価APIの本文を含まない評価メタデータ | `expiresAt`、30日 |
| `evaluationRateLimits` | 従来評価APIのrate counter | `expiresAt`、48時間 |
| `voiceRateLimits` | 音声APIのrate counter | `expiresAt`、48時間 |

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

gcloud firestore fields ttls list `
  --database="(default)" `
  --project=$ProjectId `
  --format="table(name,ttlConfig.state)"
```

3 collectionすべてが`ACTIVE`になるまで配備完了としません。TTLは即時削除の仕組みではありません。期限後の物理削除には遅延があり得るため、セキュリティ認可の期限としてTTLだけに依存しません。会話状態tokenの15分期限は、復号後にAPIが独立して検証します。

## Hosting

Web buildとHosting deployはリポジトリのscriptを使います。

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
powershell -ExecutionPolicy Bypass -File scripts/deploy-hosting.ps1 `
  -ProjectId kotae-ai-u22-2026
```

ブラウザの正式なAPI URLは同一Originの`https://kotae-ai.web.app/api/v1/voice/turns`です。Firebase HostingがCloud Runへrewriteするため、Cloud Runの`run.app` URLをクライアントへ埋め込みません。

## Consoleで確認する項目

1. Firebase Authの匿名認証と未使用匿名アカウント自動削除
2. App CheckのWeb App ID、reCAPTCHA Enterprise site key、Authenticationと独自APIの強制
3. Cloud Run revisionのruntime service account、環境変数、Secret参照、60秒timeout
4. Speech-to-Text / Text-to-Speech / Vertex AIのquotaと請求アラート
5. Firestoreの3つのTTL policyが`ACTIVE`
6. Secretへruntime以外の不要な`secretAccessor`がないこと
7. Cloud Loggingへ音声、文字起こし、prompt/response、PDF、tokenが出ていないこと

参考:

- [既存Google CloudプロジェクトへFirebaseを追加](https://firebase.google.com/docs/projects/use-firebase-with-existing-cloud-project)
- [Firebase HostingとCloud Run](https://firebase.google.com/docs/hosting/cloud-run)
- [Firebase App Check custom backend](https://firebase.google.com/docs/app-check/custom-resource-backend)
- [Cloud Speech-to-Text regional endpoints](https://cloud.google.com/speech-to-text/v2/docs/endpoints)
- [Cloud Text-to-Speech endpoints](https://cloud.google.com/text-to-speech/docs/endpoints)
- [Cloud Run Secret Manager integration](https://cloud.google.com/run/docs/configuring/services/secrets)
- [Firestore TTL](https://cloud.google.com/firestore/docs/ttl)
