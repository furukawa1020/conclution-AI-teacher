# Firebase / Google Cloud接続手順

## 現在の専用環境

- Google Cloud / Firebase project: `kotae-ai-u22-2026`
- 公開URL: `https://kotae-ai.web.app`
- Firebase Hosting: 静的Wasm UI、Passkey等の`/api/**` Cloud Run rewrite。音声stream / WebSocketは固定`run.app`へ認証付きで直接接続
- Firebase Auth: Passkey検証後に発行する仮名custom account session
- Firebase App Check: reCAPTCHA Enterprise、Passkey ceremonyと独自APIで強制
- Firestore: `(default)`、`asia-northeast1`、削除保護、ceremony・rate counter・live接続leaseのTTL
- Cloud Run: `kotae-api`、`asia-northeast1`
- Cloud Run実行ID: `kotae-api-runtime@kotae-ai-u22-2026.iam.gserviceaccount.com`
- Cloud Speech-to-Text V2: `asia-northeast1`、自然会話向け`long`単独、`ja-JP`
- Cloud Text-to-Speech: `asia-northeast1`、`ja-JP-Chirp3-HD-Kore`
- Vertex AI Native Audio: `us-central1`、標準live音声`gemini-live-2.5-flash-native-audio`
- Vertex AI文字列推論: `global`、高速`gemini-3.6-flash`、精密`gemini-3.1-pro-preview`
- Sensitive Data Protection: `asia-northeast1`、厳格音声モードの文字起こし・応答検査
- Secret Manager: `kotae-conversation-state`

FirebaseとGoogle Cloudは、別々のプロジェクトをURLやAPI keyで接続する構成ではありません。FirebaseプロジェクトはFirebase機能を追加したGoogle Cloudプロジェクトそのものです。同じproject IDのHosting、Auth、App Check、Firestore、Cloud Run、Speech-to-Text、Text-to-Speech、Vertex AI、Secret Managerを使います。

ブラウザが使うFirebase Web API keyは公開識別子であり、Cloud RunやVertex AIの権限を与える秘密鍵ではありません。保護境界はFirebase ID token、App Check、Origin検証、IAM、Secret Managerです。

## リージョン境界

| 処理 | ロケーション | 入るデータ |
|---|---|---|
| Hosting / API | Hosting / Cloud Run `asia-northeast1` | 静的asset、voice request |
| Firestore | `asia-northeast1` | TTL付き評価メタデータ・rate counter・live接続lease・Passkeyのpublic credentialと短命ceremony |
| Speech-to-Text | `asia-northeast1` regional endpoint | 厳格モードとNative Audioを使えないfallbackのraw audio。回答支援は初回・継続ともNative caption handoffを使い、2回目のSTTを使わない |
| Sensitive Data Protection | `asia-northeast1` regional endpoint | 厳格モードの文字起こしと応答文 |
| Vertex AI Native Audio | `us-central1` | 標準liveのraw audioと音声応答 |
| Vertex AI文字列推論 | `global` | 初回・継続回答支援で直接handoffしたNative input caption、fallbackの文字起こし、標準モードの短い状態要約。runtime PDFは推論前に拒否する |
| Text-to-Speech | `asia-northeast1` regional endpoint | 初回・継続caption handoff、厳格モード、Native Audioを使えないfallbackで選ばれた短い応答文。Q-ARCの有限template ID/slotは別の監査済みclosed rendererだけがcueへ変換し、その文も同じendpointでstreaming合成する。安定した暫定captionではprivate commit buffer内だけで先行可能 |
| Crossref | Google Cloud外の公開REST API | intentional turnで明示し、tool-policyとPII screenを通過したDOIまたは最小topicだけ |

標準live会話では、Cloud Runから`us-central1`のVertex AI Native Audioへraw audioを直接streamし、音声とcaptionを受け取ります。GA endpointはsetupごとに応答modalityを一つだけ許すため、`responseModalities`には`AUDIO`だけを指定し、captionは`inputAudioTranscription` / `outputAudioTranscription`を有効化して受け取ります。`TEXT`を応答modalityへ併記しません。最終入力captionが確定するまでは生成音声を利用者へ解放せず、Cloud Run内の決定論的なPII・高リスク・tool要求screenを通過した時だけcommitします。このscreenはregional DLP検査でも、Vertex AIへ送る前の原音検査でもありません。利用者が入力内で外部の質問として報告した問いへの回答支援を明示した初回turnはNative出力を破棄します。対象条件を満たす新規scopeでは、質問本文・回答候補を入力に取らない質問拘束済みQ-ARCが有限template IDとslotを選び、監査済みrendererだけがopen-slot cueへ変換します。安定した暫定captionからdecisionとstreaming TTSをprivate commit buffer内で先行できますが、空白だけを正規化した候補byte列がfinal captionと完全一致し、browser commitが確定し、同じ報告質問由来の用途分離HMACへ束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointを発行した後だけPCMを解放します。汎用scope、汎用checkpoint、cached cueは使いません。不一致なら先読みした状態とPCMを破棄してfinal captionを一度だけ処理します。それ以外はNative final input captionを監査済み文字列plannerへ直接handoffし、同じ原音を東京リージョンSTTへ再送しません。保留中の継続turnもNativeでfinal captionを確定し、監査済み段階controllerへ直接donateします。継続コーチのために同じ原音を東京STTへ二重通過させません。監査済みplanner経路では、同じ解析済み質問spanからoperatorとrequired slotを作り、確定入力の回答evidenceと用途分離した非可逆tagへ束縛するため、無関係な次turnを回答完了にしません。外部で質問された事実や現在の話者は検証せず、具体的な質問・答え・逐語録はstateやDBへ保存しません。厳格モード、Native Audioが利用できない接続fallback、高リスク・tool要求だけがraw audioから段階経路へ入り、東京リージョンSTTを一度だけ使います。runtime PDF uploadは全モードでstate decode・STT・モデル推論前にbackendが拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。提供PDFは設計参照であり、公開runtimeの添付機能ではありません。したがって、標準liveのraw audioは`us-central1`、fallbackと回答支援の文字列は`global`のVertex AIで処理され得て、明示した研究queryはCrossrefへ送られるため、「すべての会話データが日本国内だけで処理される」とは説明しません。厳格モードでは文字起こしと応答文がCloud Run内の決定論的検査とregional DLPの両方で`clear`になった時だけ後段へ進みます。

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

source buildにはruntime IDや既定Compute Engine IDを使いません。専用build IDには`roles/run.builder`だけを付与し、Vertex AI、DLP、Firebase Auth、Firestore、Speech、Secret Managerのruntime権限を付けません。`Dockerfile`のbuild/runtime baseはtagだけでなく検証したOCI index digestへ固定し、脆弱性修正版へ上げる時はmanifestを再確認してdigestを明示更新します。

```powershell
$ProjectId = "kotae-ai-u22-2026"
$BuildSaName = "kotae-api-builder"
$BuildSa = "$BuildSaName@$ProjectId.iam.gserviceaccount.com"

gcloud iam service-accounts create $BuildSaName `
  --display-name="KOTAE API source builder" `
  --project=$ProjectId

gcloud projects add-iam-policy-binding $ProjectId `
  --member="serviceAccount:$BuildSa" `
  --role="roles/run.builder"
```

既定Compute Engine IDの既存roleは、同じprojectの他workloadを確認せずに削除しません。ただしKOTAEのbuildでは今後使わず、deployごとに`--build-service-account`を明示します。

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
KOTAE_STATE_V2_WRITES=true
KOTAE_COACH_RESTATEMENT_BINDING=true
KOTAE_ANSWER_PROOF_WRITES=true
KOTAE_VERIFIER_PROGRESS_WRITES=true
KOTAE_RETRIEVAL_POLICY_ENABLED=true
KOTAE_SPEECH_LOCATION=asia-northeast1
KOTAE_SPEECH_MODEL=long
KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore
KOTAE_NATIVE_AUDIO_ENABLED=true
KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED=true
KOTAE_NATIVE_AUDIO_LOCATION=us-central1
KOTAE_NATIVE_AUDIO_MODEL=gemini-live-2.5-flash-native-audio
KOTAE_NATIVE_AUDIO_VOICE=Kore
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

`KOTAE_SPEECH_LOCATION`は実装側でも`asia-northeast1`以外を拒否します。`GOOGLE_CLOUD_LOCATION=global`は文字列Vertex専用、`KOTAE_NATIVE_AUDIO_LOCATION=us-central1`はNative Audio専用で、Speechの東京リージョンを含めて設定値を兼用しません。

既存のAuth / App Check設定を保ったまま更新する例です。4段階rolloutでは先に一度だけimageをbuildし、検証済みArtifact Registry imageを`@sha256:`付きのimmutable digestへ解決します。各段階で`--source=.`を再実行してbuildし直してはいけません。全revisionへ同じ`$ImageDigest`を渡し、変えるのはrevision名と下表の3 flagだけです。

```powershell
$Gcloud = (Resolve-Path ".\.tools\gcloud-577.0.0\google-cloud-sdk\bin\gcloud.cmd").Path
$ProjectId = "kotae-ai-u22-2026"
$RuntimeSa = "kotae-api-runtime@$ProjectId.iam.gserviceaccount.com"
$WebAppId = "<Firebase Web App ID>"
$ImageDigest = "asia-northeast1-docker.pkg.dev/$ProjectId/<repository>/kotae-api@sha256:<verified-digest>"
$StateSecretVersion = "<現在の本番revisionが参照しているnumeric version>"
$GitSha = (git rev-parse --verify HEAD).Trim()
$Stage = "reader"
$VerifierProgressWrites = "false"
$RetrievalPolicyEnabled = "false"
$NativeCaptionHandoffEnabled = "false"
$RevisionSuffix = "$Stage-$($GitSha.Substring(0, 7))-$([DateTime]::UtcNow.ToString('MMddHHmmss'))"

& $Gcloud run deploy kotae-api `
  --image=$ImageDigest `
  --project=$ProjectId `
  --region=asia-northeast1 `
  --revision-suffix=$RevisionSuffix `
  --tag="$Stage-candidate" `
  --no-traffic `
  --ingress=all `
  --allow-unauthenticated `
  --service-account=$RuntimeSa `
  --cpu=1 `
  --memory=1Gi `
  --concurrency=4 `
  --min=1 `
  --min-instances=default `
  --max=3 `
  --max-instances=3 `
  --timeout=420 `
  --remove-env-vars="KOTAE_RETRIEVAL_BELIEF_WRITES,KOTAE_SPEECH_FALLBACK_MODEL,KOTAE_COACHING_ROLLOUT,KOTAE_PRIVACY_LOCATION,KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE,KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY" `
  --update-env-vars="KOTAE_ENV=production,KOTAE_ALLOW_INSECURE_DEV=false,GOOGLE_CLOUD_PROJECT=$ProjectId,GOOGLE_CLOUD_LOCATION=global,KOTAE_ALLOWED_APP_IDS=$WebAppId,KOTAE_FAST_MODEL=vertexai/gemini-3.6-flash,KOTAE_PRECISION_MODEL=vertexai/gemini-3.1-pro-preview,KOTAE_VERTEX_PRIORITY=false,KOTAE_STATE_V2_WRITES=true,KOTAE_COACH_RESTATEMENT_BINDING=true,KOTAE_ANSWER_PROOF_WRITES=true,KOTAE_VERIFIER_PROGRESS_WRITES=$VerifierProgressWrites,KOTAE_RETRIEVAL_POLICY_ENABLED=$RetrievalPolicyEnabled,KOTAE_SPEECH_LOCATION=asia-northeast1,KOTAE_SPEECH_MODEL=long,KOTAE_SPEECH_VOICE=ja-JP-Chirp3-HD-Kore,KOTAE_NATIVE_AUDIO_ENABLED=true,KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED=$NativeCaptionHandoffEnabled,KOTAE_NATIVE_AUDIO_LOCATION=us-central1,KOTAE_NATIVE_AUDIO_MODEL=gemini-live-2.5-flash-native-audio,KOTAE_NATIVE_AUDIO_VOICE=Kore,KOTAE_REQUEST_TIMEOUT=25s,KOTAE_VOICE_TIMEOUT=50s,KOTAE_MAX_REQUEST_BYTES=32768,KOTAE_MAX_VOICE_BYTES=13631488,KOTAE_VOICE_RATE_LIMIT_PER_MINUTE=12,KOTAE_VOICE_RATE_LIMIT_PER_DAY=120,KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE=20,KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY=200,KOTAE_PASSKEY_RP_ID=kotae-ai.web.app,KOTAE_PASSKEY_ORIGIN=https://kotae-ai.web.app,KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE=10,KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY=100,KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE=300,KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY=20000,KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true" `
  --update-secrets="KOTAE_STATE_KEY_BASE64=kotae-conversation-state:$StateSecretVersion"
```

source buildはreaderの一回だけです。上のdeploy blockの`--image=$ImageDigest`を次の2引数へ置き換え、ほかの引数は同一にして実行します。実行前にworktreeがcleanであることを確認し、`& $Gcloud meta list-files-for-upload`の結果が`.gcloudignore`で許可した`Dockerfile`、`go.mod`、`go.sum`、`cmd/**`、`internal/**`とignore metadataだけであることを目視します。`.tmp*`、cache、`apps`、`dist`、`docs`、`scripts`、log、監査出力が一つでも含まれたらbuildを停止します。

```powershell
  --source=. `
  --build-service-account="projects/$ProjectId/serviceAccounts/kotae-api-builder@$ProjectId.iam.gserviceaccount.com" `
```

reader revisionがreadyになった後、そのrevisionの完全なimmutable image URLだけを採用します。`status.imageDigest`が期待したArtifact Registry hostと64桁digestでなければ停止します。writer以降では上のdeploy blockを`--image=$ImageDigest`のまま使い、`--source`やbuild service accountを再指定しません。

```powershell
$ReaderRevision = "kotae-api-$RevisionSuffix"
$Reader = (((& $Gcloud run revisions describe $ReaderRevision `
  --project=$ProjectId --region=asia-northeast1 `
  --format=json --quiet --verbosity=error) -join "`n") | ConvertFrom-Json)
$ImageDigest = [string] $Reader.status.imageDigest
if ($ImageDigest -notmatch '^asia-northeast1-docker\.pkg\.dev/kotae-ai-u22-2026/.+@sha256:[0-9a-f]{64}$') {
  throw "reader image is not an immutable production Artifact Registry URL"
}
```

`$StateSecretVersion`は配備開始前に現在の本番revisionから読み取り、4段階すべてで同じnumeric versionを維持します。rolloutの都合で`:1`や`latest`へ戻しません。鍵rotateはこのrolloutと分け、明示した別手順でだけ行います。

Native Audioのlocationは`KOTAE_NATIVE_AUDIO_LOCATION=us-central1`へ固定し、`GOOGLE_CLOUD_LOCATION=global`は文字列Vertex AI専用のまま維持します。

`--set-env-vars`や`--set-secrets`は既存設定を消す可能性があるため、再配備では現在値を確認して`--update-*`を使います。環境変数として注入するSecretは`latest`ではなく現在の本番が参照する確認済みversionへ固定し、鍵rotate時だけ別rolloutで新versionへ更新します。`KOTAE_NATIVE_AUDIO_ENABLED=true`は本番の高速会話経路を有効にし、`KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED=true`は初回だけでなく保留中の継続コーチでもNative final captionを同じ監査済みplanner/controllerへ直接donateして2回目のSTTを省く経路を有効にします。handoff flagはNative flag、verifier-progress writer、retrieval policyがすべて`true`の時だけ有効です。location、model、voiceは実装が許可する固定値から変更しません。起動時に内容を含まないNative Audio setup probeを`us-central1`へ実行するため、location、model、IAM、`AUDIO`単独の応答modality、input/output transcription configのいずれかが不適合なcandidateはreadyにならずtrafficへ昇格できません。Cloud Runのtimeoutは、アプリ側の6分live deadlineより1分長い420秒にします。これで最長4分のcapture、認証、終了処理、内部の50秒voice timeoutを収め、アプリがdeadline処理する前に基盤側が接続を切る競合を避けます。Go HTTP serverの通常routeは従来どおりread/write/idle各120秒を維持し、検証済み`/api/v1/voice/live`だけがWebSocket upgrade前に接続deadlineを6分へ延長します。deadline更新不能時はupgrade前にfail-closedで拒否します。音声と複数回のモデル呼び出しが同時にメモリへ載るため、既定の高いconcurrencyへ任せず、1 instanceあたり4 request、最大3 instanceへ明示的に制限します。最小instanceはservice単位で1にし、revision単位の最小instanceは`default`へ戻します。これにより、tag付き旧revisionをすべて常時起動する設定を残しません。

起動時のsetup probeはrevision-levelのmodel・IAM・setup検査です。`GET /health`はprocess healthだけを返すため、監視やwarmupで繰り返しても、次の利用者turnのprovider接続や`StartActivity`を準備済みにはしません。通常Native turnのWebSocket `ready`は、当該turnで改めてproviderの`SetupComplete`と`StartActivity`成功を確認した後にだけ送ります。providerを使うturnごとに接続を新規作成して終了時に閉じ、cross-turn pooling、session resumption、接続再利用は行いません。

本番では`KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true`を維持し、未指定でもsecure defaultとして`true`になります。まずcandidate backendを検証し、必須7 collectionのTTLをすべて`ACTIVE`にしてからservice rootへ昇格し、その後にPasskey UIを含むHostingを最終公開します。これにより、短命データを期限管理できないrevisionへ本番trafficを流さず、新しいUIが未対応の旧backendへ接続する時間も作りません。音声APIのbuffered、streaming、WebSocketすべてが、Passkey由来claimと5分以内の署名検証時刻`kotae_passkey_at`を要求します。Firebaseの`auth_time`はcustom token交換時に新しくなり得るため、freshness根拠には使いません。`false`は認証を迂回できるため、明示的なローカル開発以外では使いません。

`KOTAE_STATE_V2_WRITES`は短期support fieldの発行、`KOTAE_COACH_RESTATEMENT_BINDING`は言い直しtagの発行、`KOTAE_ANSWER_PROOF_WRITES`はQBA Proof用の質問インスタンスtag発行を制御します。`KOTAE_VERIFIER_PROGRESS_WRITES`は、現在の質問に対する検証の進行だけを表す5個の固定小数verifier-progress audit-posterior massをstateへ発行するwriter flagです。本人のretrieval状態を推定するflagではありません。`KOTAE_RETRIEVAL_POLICY_ENABLED`は、質問拘束済みQ-ARCと回答後の有限型controllerを使うbehavior flagであり、writer flagとは独立です。`KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED`はNative final captionを監査済みplanner/controllerへ直接donateするbehavior flagです。policyを`true`にするには`KOTAE_STATE_V2_WRITES=true`、`KOTAE_ANSWER_PROOF_WRITES=true`、`KOTAE_COACH_RESTATEMENT_BINDING=true`が必要です。これによりA-laterは設定で即時completeへ迂回できず、質問boundな一度だけの再質問を必ず使います。caption handoffを`true`にするにはさらにverifier-progress writerとretrieval policyがともに`true`でなければなりません。長期運用ではこの6 flagをすべて`true`にします。progressには質問、回答、逐語録、診断、人物特性を入れません。本番Hostingのpreflightは、昇格済みCloud Run revisionで`KOTAE_VERIFIER_PROGRESS_WRITES=true`、`KOTAE_RETRIEVAL_POLICY_ENABLED=true`、`KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED=true`を必須とし、一つでも欠ける、または`false`なら公開前に停止します。

verifier progress、policy、Native caption handoffは次の4段階でreader-first移行します。tupleの順序は常に`(KOTAE_VERIFIER_PROGRESS_WRITES, KOTAE_RETRIEVAL_POLICY_ENABLED, KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED)`です。

| 段階 | tuple | 動作 |
|---|---|---|
| **reader** | `(false, false, false)` | 新fieldとpolicy versionを読んで検証できる新binaryをcandidate検証し、revision名を指定して100%へ昇格する |
| **writer** | `(true, false, false)` | 同一digestの別revisionで新progressだけを発行する。会話行動は旧policyのまま |
| **policy** | `(true, true, false)` | 同一digestの別revisionでQ-ARCと回答後controllerを有効にする。Native caption handoffはまだ閉じる |
| **caption** | `(true, true, true)` | 同一digestの別revisionで初回・継続Native final captionの直接handoffを有効にし、最終candidate検証後に100%へ昇格する |

各段階で上のdeploy例の`$Stage`と3変数だけを表どおり変更し、`$ImageDigest`と`$StateSecretVersion`は固定します。各revisionをtag URLでcandidate検証し、前段を100%へ昇格してから次段を作ります。readerを100%へ昇格した後は、旧binaryへ接続済みの最長420秒WebSocketを排出するため430秒以上待ち、30〜60秒間隔で旧revisionのrequest/logを監視してからwriterを作ります。このdrainを省略して新fieldを書き始めません。

captionだけは100%へ直行させません。candidate検証後、明示したrevision名で`policy=99 / caption=1`、`policy=90 / caption=10`、`caption=100`の順にtrafficを動かし、各段階で`/health`、startup probe、error log、first-meaningful-audio telemetryを確認します。異常時は直前の同一digest revisionへ100%を明示して戻し、opaque sessionを破棄します。`--to-latest`は使いません。

```powershell
& $Gcloud run services update-traffic kotae-api --project=$ProjectId --region=asia-northeast1 --clear-tags --to-revisions="$PolicyRevision=99,$CaptionRevision=1"
# 観測後
& $Gcloud run services update-traffic kotae-api --project=$ProjectId --region=asia-northeast1 --clear-tags --to-revisions="$PolicyRevision=90,$CaptionRevision=10"
# 観測後
& $Gcloud run services update-traffic kotae-api --project=$ProjectId --region=asia-northeast1 --clear-tags --to-revisions="$CaptionRevision=100"
```

最終昇格前後に4 revisionを再取得し、`status.imageDigest`のunique数と`KOTAE_STATE_KEY_BASE64`のnumeric keyのunique数がそれぞれ1で、Secret keyが開始時の`$StateSecretVersion`と一致することをassertします。また`KOTAE_RETRIEVAL_BELIEF_WRITES`、`KOTAE_SPEECH_FALLBACK_MODEL`、`KOTAE_COACHING_ROLLOUT`、`KOTAE_PRIVACY_LOCATION`、`KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE`、`KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY`の6変数が全revisionから消えていなければ公開を停止します。最終trafficはcaption revisionだけ100%、tagなしでなければなりません。

writer以降に戻せるのは新fieldを読める同一digestのreader以降のrevisionだけです。caption段階からのbehavior rollbackは同一digestのpolicy revisionへ、policyからはwriterへ戻せますが、opaque stateは破棄して新しいセッションから始めます。これにより新しい認証暗号stateが旧readerへ届く時間を作りません。privacy境界より前のtokenは同じ鍵でも復号させないため、token prefixとAADを`v2`へ切り替えており、旧`v1`とのdual-readはしません。切替時に進行中の最長15分の会話は再開できず、利用者は新しいセッションを開始します。これは移行の不便より、境界導入前の会話由来stateを再びモデルへ渡さないことを優先したものです。

rollback時も`v1`と`v2`の状態は相互利用しません。revisionを戻した後はブラウザのopaque stateを破棄して新しいセッションから始めます。

このdeployはcandidate revisionをtag URLへ公開しますが、service rootのtrafficは変更しません。candidateの`/health`と未認証`/api/v1/me`境界を確認し、さらに`configure-firestore-ttl.ps1`が必須7 policyの`ACTIVE`を確認した後だけ、`& $Gcloud run services update-traffic kotae-api --project=$ProjectId --region=asia-northeast1 --clear-tags --to-revisions="kotae-api-$RevisionSuffix=100"`で検証したrevision名へtrafficを移し、全tag URLを同時に外します。caption以外の段階は100%、captionは前述の1%→10%→100% canaryを使います。`--to-latest`は将来のrevisionへ自動追従するため使いません。revision自体はtagなし・0% trafficで残し、rollback時だけservice rootのtrafficを明示的に戻します。公開の旧tag URLは新しいprivacy境界を迂回できるため残しません。Hosting scriptも、service rootが最新ready revisionへ100%昇格済みでPasskey gateが`true`、timeout・service account・build identityが固定値、必須TTLがすべて`ACTIVE`、`/health`が正常であることを再検証してからreleaseを作ります。

Firebase HostingのCloud Run rewriteは、Cloud Run IAM用のID tokenを付けない公開transportです。そのため`kotae-api`は`--ingress=all --allow-unauthenticated`を維持します。これはAPI認証を無効にする設定ではありません。`/api/**`はアプリ側でFirebase ID tokenとApp Check tokenの両方、許可App ID、厳密なOrigin、二段rate limitを検証します。`--no-allow-unauthenticated`または`--ingress=internal-and-cloud-load-balancing`へ変更すると、Hostingからコンテナへ届かず汎用404になります。

UID単位の枠に加え、許可App ID全体のvoice枠をbody decode前に消費します。仮名accountを作り直してもproject全体の費用上限を素通りできないための二段目です。Passkeyの4 ceremony APIは、client単位の10/分・100/日と、App ID全体の300/分・20,000/日のサーキットブレーカーを別collectionで消費します。旧`KOTAE_PASSKEY_APP_RATE_LIMIT_*`は意味が曖昧なため互換扱いせず、残っているrevisionは起動時に移行エラーになります。配備時に旧変数を削除して新しい4変数を同時に設定します。live WebSocketは認証とrate枠の消費後、音声受信と`ready`送信の前にFirestore transactionでUID単位のleaseを取得し、同じFirebase UIDの同時接続をproject全体で1本に制限します。この順序により、同じ認証済みclientが重複handshakeを繰り返す場合もrate limitを迂回できません。接続終了時はpipelineをcancelし、終了signalを最大5秒待ちます。終了を確認できた時だけ所有者を照合してleaseを解放し、5秒以内に停止しない時は即時解放せず7分の`expiresAt`まで保持します。instance消失やFirestore release失敗でもdocumentは残り、transaction内の期限判定で7分後から再取得できます。Firestoreの取得・更新に失敗した場合はlive接続だけをfail-closedで拒否し、buffered HTTPS voiceの経路はこのleaseを要求しません。

providerを使う標準Native turnではlease取得後にpipelineを起動し、そのturnの`SetupComplete`と`StartActivity`成功まで`ready`とPCM読取を保留します。ブラウザも`ready`まではtrackをmuteし、capture graph、VAD、`Listening`を開始しません。Native live preflightが4,000 ms以内にreadyにならなければlive接続を閉じ、PCM capture・VAD開始前に認証付きHTTP fallbackへ移ります。開始操作から`Listening`までのcontent-free prepare SLOは、1,000 ms以下をon-target、1,000 ms超3,000 ms未満をslow、3,000 ms以上4,000 ms未満をmissed、4,000 ms以上をtimed-outとして、speech-endから有意味PCMまでの既存1秒・3秒・10秒SLOと別に確認します。

barge-in候補は開始済みsessionの端末内VAD、bounded MediaRecorder、対応端末の固定長PCM ringで保持します。AEC確認済み・stateful drain不要のpre-final Native live・PCM handoffがそろう時だけ新providerを裏で準備し、adopt・送信はstrong ready後に限定します。発話終了時点でreadyでなければ最大450 msだけ待ち、HTTP fallbackへ一方向に切り替えて準備中providerをcancelします。この準備経路ではroute確定前に新turnを`Listening`へしません。それ以外のbarge-inは新providerを準備せず、MediaRecorderのHTTP経路として直ちに進みます。

未認証live handshakeにはinstanceごとのnon-blockingな2 slot gateを使います。2 slotが使用中ならWebSocketへupgradeする前にHTTP 429を返し、受け入れた接続にも最初のframeを2秒以内に要求します。Firebase ID tokenとApp Check tokenの検証が完了した直後にslotを解放するため、通常のlive sessionは保持しません。`--concurrency=4`のうち、gateが待機を許す未認証handshakeは最大2 request（最大1/2）で、未認証handshakeだけでは残り2 request slotを占有できません。これはinstance内resourceの占有上限であってedge DDoS防御ではなく、最初のframeのcredentialも一回限りticketではありません。App Checkは不正利用を減らしますが、Web attestationや通常tokenがすべての濫用を防ぐ保証はありません。Go Admin SDKではcustom backend向けlimited-use token消費が未対応のため、現在は再利用可能なtoken検証、二段rate limit、厳密なOrigin、Cloud Run上限、Google Cloud quotaと請求アラートを重ねます。

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
| `voiceLiveLeases` | SHA-256化UIDとランダム所有者だけを持つlive同時接続lease。音声・文字起こし・raw UIDは保存しない | `expiresAt`、最長7分 |

```powershell
powershell -ExecutionPolicy Bypass -File scripts/configure-firestore-ttl.ps1 `
  -ProjectId kotae-ai-u22-2026
```

このscriptは表のうちTTLを持つ7 collectionの`expiresAt`を冪等に有効化し、一定間隔で状態を確認します。7つすべてが`ACTIVE`になってexit code 0で終了するまでは、candidateをservice rootへ昇格せず、Hostingも公開しません。timeout、CLI失敗、不正なJSON、必須policyの欠落はすべて非0で失敗します。TTLは即時削除の仕組みではありません。期限後の物理削除には遅延があり得るため、ceremonyは読み出しtransactionで単回消費し、5分期限もAPIが独立検証します。live leaseもtransaction内で`expiresAt`を検証し、期限を過ぎたdocumentを削除待ちでも上書き取得できます。会話状態tokenの15分期限も、復号後にAPIが独立して検証します。

## Hosting

Web buildとHosting deployはリポジトリのscriptを使います。

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1 `
  -ExpectedGitCommit (git rev-parse HEAD)
powershell -ExecutionPolicy Bypass -File scripts/deploy-hosting.ps1 `
  -ProjectId kotae-ai-u22-2026 `
  -ExpectedGitCommit (git rev-parse HEAD) `
  -PreflightOnly
powershell -ExecutionPolicy Bypass -File scripts/deploy-hosting.ps1 `
  -ProjectId kotae-ai-u22-2026 `
  -ExpectedGitCommit (git rev-parse HEAD)
```

Hosting release buildは、開始時と完了時のcleanな作業ツリー、`HEAD`、指定commitを照合し、全公開artifactのSHA-256とbyte数をrelease manifestへ固定します。preflightと本番deployは、指定commitが`HEAD`と`origin/main`の両方に一致し、manifestと公開artifactが完全一致しない限りCloud APIを呼びません。検証済みbyte列のsnapshotだけをuploadし、manifest自体は公開しません。引数なしの`build-web.ps1`はローカル確認用で、Hostingへreleaseできません。

Passkey、`/me`等の通常APIは`https://kotae-ai.web.app/api/**`を使い、Firebase HostingがCloud Runへrewriteします。低遅延音声はHosting rewriteがWebSocketを中継しないため、固定した`https://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/turns:stream`と`wss://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/live`へ直接CORS/TLSで接続します。Cloud Run側はFirebase token、App Check、exact Hosting Origin、mode、quotaを検証します。Cloud RunではWebSocketも長時間HTTP requestとしてrequest timeoutの対象になるため、`420`秒は永続接続の保証ではありません。切断後の再接続は新しいrequestとして扱い、確定済み音声を自動再送しません。同期圧縮HTTP fallbackの音声は2 MiB上限なので、3分級の発話を処理できるとは保証しません。

## Consoleで確認する項目

1. Passkey登録・認証が公開originで成功し、cancel / unsupported時にマイクを取得しないこと
2. App CheckのWeb App ID、reCAPTCHA Enterprise site key、Passkey ceremonyと独自APIの強制
3. Cloud Run revisionのruntime service account、専用build service account、Passkey/DLP環境変数、Native Audioの有効化・固定location・固定model・固定voice、固定Secret version参照、420秒request timeout
4. Speech-to-Text / Text-to-Speech / Vertex AI / DLPのquotaと請求アラート
5. Firestoreの7つのTTL policyが`ACTIVE`
6. runtime自身以外へ不要な`serviceAccountTokenCreator`がなく、Secretへruntime以外の不要な`secretAccessor`がないこと
7. Cloud Loggingへ音声、文字起こし、prompt/response、PDF、token、WebAuthn responseが出ていないこと
8. `KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE=true`で全音声transportが古い・非Passkey tokenを拒否すること
9. runtime service accountに`roles/dlp.user`があり、起動時の固定DLP readiness probeを通過すること

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
