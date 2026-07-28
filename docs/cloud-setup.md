# Firebase / Google Cloud 接続手順

## 現在の専用環境

- 公開URL: `https://kotae-ai.web.app`
- Firebase / Google Cloud project ID: `kotae-ai-u22-2026`
- Firestore: `(default)`、`asia-northeast1`、削除保護あり
- Cloud Run: `kotae-api`、`asia-northeast1`
- Firebase Web App ID: `1:551920539470:web:6518baf6d84d7ab89eb01f`

Google Cloudのproject IDは作成後に変更できません。コンテスト名をブランドへ露出させないため、Hosting site IDを別に `kotae-ai` とし、project IDは内部識別子としてだけ使います。

FirebaseとGoogle Cloudは別プロジェクトを「接続」するものではありません。FirebaseプロジェクトはFirebase機能が追加されたGoogle Cloudプロジェクトそのものです。同一project IDをHosting、Auth、Firestore、Cloud Run、Vertex AIで使います。

現在このPCに残っているgcloud既定プロジェクト `improve-production-management` は別用途と判断し、変更対象にしません。

## 今後ユーザーがブラウザで行う操作

1. Google認証のOAuth同意画面に表示するアプリ名とサポートメールを確定する。
2. reCAPTCHA Enterpriseの課金・利用条件を確認してApp Checkを有効化する。
3. 音声保存、後日再評価、共有、品質改善を分離した同意文面を承認する。

Firebase利用規約、課金接続、専用project作成、Firebase追加、必要API有効化までは完了しています。

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
