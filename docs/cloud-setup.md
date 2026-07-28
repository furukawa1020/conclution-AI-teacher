# Firebase / Google Cloud 接続手順

## 先に確定する三項目

1. 専用Google CloudプロジェクトID  
   例: `kotae-ai-dev-任意の短い識別子`。プロジェクトIDは後から変更できません。
2. 課金アカウント  
   Cloud Run、Vertex AI、Cloud KMSを使うためBlaze相当の課金接続が必要です。
3. Firestoreロケーション  
   日本利用を前提に `asia-northeast1` を推奨します。作成後の変更は簡単ではないため、既存の無関係なプロジェクトでは作りません。

FirebaseとGoogle Cloudは別プロジェクトを「接続」するものではありません。FirebaseプロジェクトはFirebase機能が追加されたGoogle Cloudプロジェクトそのものです。同一project IDをHosting、Auth、Firestore、Cloud Run、Vertex AIで使います。

現在このPCに残っているgcloud既定プロジェクト `improve-production-management` は別用途と判断し、変更対象にしません。

## ユーザーがブラウザで行う操作

1. [Firebase Console](https://console.firebase.google.com/) を開き、Firebase利用規約を確認・同意する。
2. [Google Cloud Billing](https://console.cloud.google.com/billing) で利用可能な課金アカウントIDを確認する。
3. 新しい専用project IDを決める。
4. Google認証のOAuth同意画面に表示するアプリ名とサポートメールを決める。

この四点が揃ったら、CLIによる作成・接続・API有効化を進められます。Firebase CLIの `projects:addfirebase` は既存Google CloudプロジェクトへFirebaseを追加する公式コマンドです。

## CLI

このPCには現在 `gcloud` と `firebase` がPATH上にありません。インストール後、次を実行します。

```powershell
gcloud auth login
gcloud auth application-default login
firebase login
gcloud auth list
firebase login:list
```

サービスアカウントJSON鍵はダウンロードしません。ローカルはユーザーADC、本番はCloud RunのサービスIDを使います。

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
