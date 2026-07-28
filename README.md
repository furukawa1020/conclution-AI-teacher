# コタエーAI

質問に対して「最初の一文で答える」力を、発話の時間構造と文章の意味の両方から鍛える対話コーチです。

U-22プログラミング・コンテスト向けに、一般的なチャットUIではなく次の独自技術を核にします。

- Rust/Wasmでマイク音声から発話開始、沈黙、言い直しの時間特徴を端末内抽出
- 録音チャンクを端末内でAES-256-GCM暗号化し、改ざん・欠落・並べ替えを検出
- 同じRustコアをWebとAndroidで共有
- Go＋Genkit＋Vertex AIで、意味的な「結論先出し」を構造化判定
- Firebase AuthとApp Checkを両方検証し、Firestoreはブラウザから直接アクセスさせない

## 構成

```text
apps/client         Rust + Dioxus 0.7 Web/Wasm UI
crates/audio_core   生音声を保持しない発話タイミング解析
crates/audio_vault  録音チャンクの暗号化と完全性検証
cmd/api             Cloud Run向けGo API
internal            Firebase認証、Genkit評価、Firestore保存
docs                クラウド・音声セキュリティ設計
```

## ローカル検証

```powershell
go test ./...
cargo test --workspace
```

Web版はDioxus CLI 0.7.9を使います。

```powershell
dx serve --package kotae-client --platform web
```

クラウドを作る前に [クラウド接続手順](docs/cloud-setup.md) を読み、専用プロジェクトID、課金先、Firestoreロケーションを確定してください。既存の無関係なGoogle Cloudプロジェクトへは接続しません。

## セキュリティ原則

- サービスアカウントJSON鍵を作らず、Cloud RunのサービスIDを使う
- 音声、文字起こし、回答本文、トークン、暗号鍵をログへ出さない
- 音声の保存・再評価・共有・学習利用を別々に同意する
- 「管理型セキュア履歴」と、本人解除が必要な「Vaultモード」を区別する
- TypeScriptは使わない。JavaScriptはブラウザAPIとFirebase SDKの細い境界だけに限定する

詳細は [音声セキュリティ設計](docs/audio-security.md) を参照してください。
