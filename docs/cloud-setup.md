# Firebase / Google Cloud 接続手順

## 現在の専用環境

- 公開URL: `https://kotae-ai.web.app`
- Firestore: `(default)`、`asia-northeast1`、削除保護あり、評価30日・レート制限48時間のTTL
- Cloud Run: `kotae-api`、`asia-northeast1`
- Firebase Auth: 匿名認証、30日後の未使用アカウント自動削除
- Firebase App Check: reCAPTCHA Enterprise、Authenticationと独自APIの両方で強制

Google Cloudのproject IDは作成後に変更できません。公開ブランドとURLは `コタエーAI / kotae-ai` に統一し、既存project IDは誤配備防止のため設定ファイルとデプロイスクリプト内だけで照合します。

FirebaseとGoogle Cloudは別プロジェクトを「接続」するものではありません。FirebaseプロジェクトはFirebase機能が追加されたGoogle Cloudプロジェクトそのものです。同一project IDをHosting、Auth、Firestore、Cloud Run、Vertex AIで使います。

## 今後ユーザーがブラウザで行う操作

1. Googleログインを追加する段階で、OAuth同意画面のアプリ名とサポートメールを確定する。
2. 音声履歴を実装する前に、保存、後日再評価、共有、品質改善を分離した同意文面を承認する。
3. Firebase / Vertex AI / reCAPTCHA Enterpriseの利用量と請求アラートを定期確認する。

課金接続、Firebase追加、必要API、Hosting、Auth、App Check、Firestore、Cloud Run、Vertex AIの接続は完了しています。

## CLI

必要なCLIはリポジトリ内のインストールスクリプトで検証します。

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-tools.ps1
```

サービスアカウントJSON鍵はダウンロードしません。本番はCloud Runの専用サービスIDを使います。すべての変更コマンドは対象projectを明示し、gcloudの既定projectには依存しません。

## 作成時の順序

```powershell
gcloud projects create PROJECT_ID --name="KOTAE AI"
gcloud billing projects link PROJECT_ID --billing-account=BILLING_ACCOUNT_ID
gcloud services enable firebase.googleapis.com serviceusage.googleapis.com --project=PROJECT_ID
firebase projects:addfirebase PROJECT_ID
```

Firebase利用規約未同意、または必要権限不足の場合、`projects:addfirebase` は403になります。その場合は権限を広げる前に、Console上の利用規約と対象project IDを確認します。

続いて必要APIとFirestoreを作成します。

```powershell
gcloud services enable `
  run.googleapis.com `
  artifactregistry.googleapis.com `
  cloudbuild.googleapis.com `
  aiplatform.googleapis.com `
  firestore.googleapis.com `
  identitytoolkit.googleapis.com `
  firebaseappcheck.googleapis.com `
  recaptchaenterprise.googleapis.com `
  cloudkms.googleapis.com `
  storage.googleapis.com `
  cloudtasks.googleapis.com `
  logging.googleapis.com `
  monitoring.googleapis.com `
  --project=PROJECT_ID

gcloud firestore databases create `
  --database="(default)" `
  --location=asia-northeast1 `
  --type=firestore-native `
  --delete-protection `
  --project=PROJECT_ID
```

Firestoreロケーションを確定する前に、最後のコマンドは実行しません。

## Cloud Run実行ID

最初の評価APIには専用サービスIDを作り、Vertex AIとFirestoreだけを許可します。

```powershell
gcloud iam service-accounts create kotae-api `
  --display-name="KOTAE evaluation API" `
  --project=PROJECT_ID

gcloud projects add-iam-policy-binding PROJECT_ID `
  --member="serviceAccount:kotae-api@PROJECT_ID.iam.gserviceaccount.com" `
  --role="roles/aiplatform.user"

gcloud projects add-iam-policy-binding PROJECT_ID `
  --member="serviceAccount:kotae-api@PROJECT_ID.iam.gserviceaccount.com" `
  --role="roles/datastore.user"
```

音声用のLive Gateway、Cipher Gateway、Key Broker、Re-eval WorkerはサービスIDを分けます。StorageとKMSの両方へ直接アクセスできる単一サービスを作りません。

## デプロイ前に必要な値

- Firebase Web App ID
- reCAPTCHA Enterprise site key
- `KOTAE_ALLOWED_APP_IDS` に許可するWeb/Android App ID
- Cloud Runリージョン `asia-northeast1`
- Vertex AIロケーション。データ所在地を優先する場合は利用モデルの日本リージョン対応を確認する

Cloud RunのREST APIはFirebase Hostingから `/api/**` だけrewriteします。長時間のLive WebSocketはHostingを経由させません。

参考:

- [既存Google CloudプロジェクトへFirebaseを追加](https://firebase.google.com/docs/projects/use-firebase-with-existing-cloud-project)
- [Firebase CLI](https://firebase.google.com/docs/cli)
- [Firestoreデータベース作成](https://cloud.google.com/firestore/docs/manage-databases)
- [Firebase HostingとCloud Run](https://firebase.google.com/docs/hosting/cloud-run)
