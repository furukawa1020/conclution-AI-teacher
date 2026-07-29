# コタエーAI

固定された質問へ答える練習ではなく、話している内容から「いま本当に答えるべき問い」を推定し、答えの核が抜けた時だけ短く返す音声ベースの思考コーチです。日常のぼやき、考え途中の独り言、研究や論文の話を対象にし、本人の言い直しで解決しそうな時は割り込みません。

公開版: [https://kotae-ai.web.app](https://kotae-ai.web.app)

## 作品の核

- **KOTAE Reflex**: 発話から目標、主張、根拠、制約を短いThought State Graphとして更新し、自己修正を待つ時間とExpected Value of Intervention（EVI）を使って、話すか沈黙するかを決める
- **Latent Answer Contract（LAC）**: 潜在的な問いを最大3候補まで仮説化し、問いが要求する型と回答冒頭のコミットメントを照合する。問いが曖昧なら決めつけず、答えの核が後ろへ埋もれた時だけ、条件と不確実性を変えない再構成を許可する
- **音声向け出力**: 内部の分析を画面へ大量表示せず、必要な一つの介入だけを短い日本語音声へ変換する

KOTAE ReflexとLACは、このプロジェクトで設計・実装している実験的な仕組みです。近接研究を踏まえていますが、「世界初」や有効性が確立済みとは表現しません。新規性の評価は、既存APIの組み合わせではなく、潜在問いの同定、答えの先頭契約、意味保存を伴う修復が実測で機能するかによって行います。

## 現在の音声経路

```text
Rust / Dioxus / Wasm UI
  └─ ブラウザJavaScript境界: MediaRecorder、VAD、Firebase SDK
       └─ POST /api/v1/voice/turns
            └─ Cloud Run / Go
                 ├─ Cloud Speech-to-Text V2（asia-northeast1）
                 ├─ Vertex AI（global）: KOTAE Reflex + LAC
                 └─ Cloud Text-to-Speech（asia-northeast1）
```

マイクは利用者が明示的に開始したセッション中だけ使います。端末側VADが一つの発話を区切り、音声を同一オリジンAPIへ送ります。Cloud Speech-to-Textで得た文字列と、利用者が今回だけ添付したPDFはVertex AIへ送られる場合があります。応答する価値が低ければ音声を返さず、そのまま聞き続けます。

音声、文字起こし、モデル応答、PDFはアプリのFirestore、Cloud Storage、ログへ保存しません。会話を続けるための短い意味要約だけをAES-256-GCMで暗号化した不透明な状態トークンとしてブラウザメモリへ返します。これはE2EEではなく、Cloud Run、Speech-to-Text、Vertex AI、Text-to-Speechの処理中には各サービスが必要な平文を扱います。正確な境界は [音声セキュリティ設計](docs/audio-security.md) を参照してください。

## 構成

```text
apps/client              Rust + Dioxus 0.7 Web/Wasm UI
apps/client/web          ブラウザAPIとFirebase SDKだけを扱うJavaScript境界
crates/audio_core        Rust VAD実験実装。現在の公開capture経路には未接続
crates/audio_vault       将来研究用の暗号化コア。現在の公開音声経路には未接続
cmd/api                  Cloud Run向けGo API
internal/conversation    Thought State Graph、EVI、モデル経路、暗号化状態
internal/answercontract  LACの決定論的な検証と意味保存ガード
internal/speechio        東京リージョンのSTT / TTS境界
internal/voiceflow       音声認識、推論、音声合成の一時処理
docs                     クラウド、セキュリティ、研究設計
```

TypeScriptは使いません。JavaScriptは、ブラウザから直接必要なMediaRecorder、Web Audio、Firebase Web SDKをRust/Wasmへ橋渡しする範囲に限定します。認証、入力検証、推論、状態暗号化、LACはGoまたはRust側です。

## ローカル検証

```powershell
go test ./...
cargo test --workspace --locked
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
```

クラウド構成と再配備手順は [Firebase / Google Cloud接続手順](docs/cloud-setup.md)、推論機構の設計と評価計画は [KOTAE Reflex設計](docs/kotae-reflex.md) に記録しています。

## セキュリティ原則

- Firebase ID token、Firebase App Check、同一Originをすべて検証する
- サービスアカウントJSON鍵を作らず、Cloud Runの専用サービスIDを使う
- 音声、文字起こし、モデル応答、PDF、token、秘密鍵をアプリログへ出さない
- STT / TTSは`asia-northeast1`のリージョナルエンドポイントへ固定する
- Vertex AIは`global`であり、文字起こしと添付PDFが日本リージョン内に限定されるとは説明しない
- PDFは一つのターンだけ送信し、原文ではなく短い意味要約だけが暗号化状態へ残り得る
- 状態鍵はSecret Managerで管理し、状態トークンはFirebase UIDへ束縛して15分で失効させる
- 音声履歴、再生履歴、無人の後日再評価、保存音声Vaultは現在の公開経路に実装していない
