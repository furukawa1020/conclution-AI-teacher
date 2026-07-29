# コタエーAI

質問に対して「最初の一文で答える」力を、発話の時間構造と文章の意味の両方から鍛える対話コーチです。

公開版: [https://kotae-ai.web.app](https://kotae-ai.web.app)

- Rust/Wasmでマイク音声から発話開始、沈黙、言い直しの時間特徴を端末内抽出
- 現在の公開版は音声を保存・送信せず、短命PCMも測定終了時にゼロクリア
- Go＋Genkit＋Vertex AIで、意味的な「結論先出し」を構造化判定
- Firebase AuthとApp Checkを両方で強制し、Firestoreはブラウザから直接アクセスさせない
- 回答本文・質問本文・音声は保存せず、Firestoreには30日TTL付きの評価メタデータだけを保存

## 構成

```text
apps/client         Rust + Dioxus 0.7 Web/Wasm UI
crates/audio_core   生音声を保持しない発話タイミング解析
crates/audio_vault  将来の同意制履歴向け暗号化・完全性検証コア（公開版では未接続）
cmd/api             Cloud Run向けGo API
internal            Firebase認証、レート制限、Genkit評価、メタデータ保存
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

クラウド構成と再配備手順は [クラウド接続手順](docs/cloud-setup.md) に記録しています。既存の無関係なGoogle Cloudプロジェクトには接続しません。

## セキュリティ原則

- サービスアカウントJSON鍵を作らず、Cloud RunのサービスIDを使う
- 音声、文字起こし、回答本文、トークン、暗号鍵をログへ出さない
- 現在は音声を保存しない。将来実装する場合は保存・再評価・共有・学習利用を別々に同意する
- 将来の「管理型セキュア履歴」と、本人解除が必要な「Vaultモード」を区別する
- TypeScriptは使わない。JavaScriptはブラウザAPIとFirebase SDKの細い境界だけに限定する

詳細は [音声セキュリティ設計](docs/audio-security.md) を参照してください。
