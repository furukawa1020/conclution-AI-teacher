# コタエーAI

コタエーAIは、本人の答えを代作したり、採点して一律に矯正したりするアプリではありません。相手から聞かれた質問と、まとまらないまま話した本人の回答を分けて捉え、「A」を本人自身の言葉で先に返せるよう、普通の音声会話の中で必要な時だけ短く支えます。会話を課題や訓練にせず、一往復だけでも終えられます。この会話支援が回答能力を長期に向上させることはまだ実証していません。

公開版: [https://kotae-ai.web.app](https://kotae-ai.web.app)

## 作品の核

- **KOTAE Reflex**: 発話から目標、主張、根拠、制約を短いThought State Graphとして更新し、自己修正を待つ時間とExpected Value of Intervention（EVI）を使って、話すか沈黙するかを決める
- **Latent Answer Contract（LAC）**: 潜在的な問いを最大3候補まで仮説化し、問いが要求する型と回答冒頭のコミットメントを照合する。問いが曖昧なら決めつけず、答えの核が後ろへ埋もれた時だけ、条件と不確実性を変えない再構成を許可する
- **Respondent Coach + Meaning Gate**: 「AIへの質問」と「相手から本人へ向けられた質問」を区別する。後者では本人の回答内に完全一致するslot evidenceを束縛して判定するが、AIの再構成案を本人の答えとして読み上げない。Aが後ろなら固定文で一度だけやさしく聞き直し、次は普通の会話へ戻る。Aを先に言えたら通常はそこで閉じ、本人が同じ支援中に厳密句「理由まで一問お願いします」と明示した時だけ、理由・根拠・最初の一歩のうち質問の型に合う一問を足す。この任意の一問は二段目の合格試験にせず、最初の実質的な返答で閉じる。考え中の「えっと」「うーん」は失敗回数にせず、無監査のモデル文ではなく固定の短い相づちを返す
- **Research discovery**: 本人のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した時だけ、固定語の間にあるtopicまたはbare DOIをCrossrefへ最小送信する。返すのはCrossrefの索引日が指定期間内の書誌候補であり、論文の発表日順でも、本文や主張を検証済みとした結果でもない
- **音声向け出力**: 内部の分析を画面へ大量表示せず、必要な一つの介入だけを短い日本語音声へ変換する

KOTAE ReflexとLACは、このプロジェクトで設計・実装している実験的な仕組みです。近接研究を踏まえていますが、「世界初」や有効性が確立済みとは表現しません。新規性の評価は、既存APIの組み合わせではなく、潜在問いの同定、答えの先頭契約、意味保存を伴う修復が実測で機能するかによって行います。

## 現在の音声経路

```text
Rust / Dioxus / Wasm UI
  └─ ブラウザJavaScript境界: MediaRecorder、VAD、Firebase SDK
       └─ 固定Cloud Run URLへ認証付きCORS / TLS
            └─ Cloud Run / Go
                 ├─ Cloud Speech-to-Text V2（asia-northeast1）
                 ├─ Sensitive Data Protection（asia-northeast1）
                 ├─ 検査・置換後の文字列 → Vertex AI（global）
                 ├─ Crossref（明示したDOI / 新着論文検索だけ）
                 └─ Cloud Text-to-Speech（asia-northeast1）
```

マイクは利用者が明示的に開始したセッション中だけ使います。音声は固定Cloud Run URLへ直接CORS/TLSで送り、Speech-to-Textで得た文字列は東京リージョンのSensitive Data Protectionで検査・置換できた場合だけVertex AIへ進めます。検査不能時は元の文字列へfallbackせず停止します。確定前の文字起こしを使う投機推論も本番では停止します。PDFは同じ境界で原本全体を安全に変換できないため、クライアントがファイルを読む前とAPIがモデルを呼ぶ前の両方で拒否します。

原音、文字起こし、モデル応答、PDF本文、研究query・候補はKOTAEのFirestore、Cloud Storage、アプリログへ保存しません。これはGoogle Cloud全体の絶対的なゼロ保持保証ではありません。Cloud Run、Speech-to-Text、Sensitive Data Protection、Vertex AI、Text-to-Speechは処理に必要な平文を扱うためE2EEではなく、検出器には漏れがあり得るため完全なPII除去でもありません。応答文もTTS前に同じ保護境界を通し、置換された応答を含む旧状態tokenは公開しません。正確な境界は [音声セキュリティ設計](docs/audio-security.md) を参照してください。

## 構成

```text
apps/client              Rust + Dioxus 0.7 Web/Wasm UI
apps/client/web          ブラウザAPIとFirebase SDKだけを扱うJavaScript境界
crates/audio_core        Rust VAD実験実装。現在の公開capture経路には未接続
crates/audio_vault       将来研究用の暗号化コア。現在の公開音声経路には未接続
cmd/api                  Cloud Run向けGo API
internal/conversation    Thought State Graph、EVI、モデル経路、暗号化状態
internal/answercontract  LACの決定論的な検証と意味保存ガード
internal/respondent      本人回答のexact evidence gateと決定論的回答コーチ
internal/research        固定sourceの論文書誌探索と「未検証」型
internal/privacyguard    ローカル検査 + regional DLPのfail-closed境界
internal/speechio        東京リージョンのSTT / TTS境界
internal/voiceflow       音声認識、推論、音声合成の一時処理
docs                     クラウド、セキュリティ、研究設計
```

TypeScriptは使いません。JavaScriptは、ブラウザから直接必要なMediaRecorder、Web Audio、Firebase Web SDKをRust/Wasmへ橋渡しする範囲に限定します。認証、入力検証、推論、状態暗号化、LACはGoまたはRust側です。

## Firebase Authenticationの必須設定

クライアントは匿名認証を作らず、検証済みGoogle providerのFirebase ID tokenだけを使います。これは「そのGoogleアカウントを現在使える」ことの確認であり、法的な本人確認、声紋照合、現在話している人の本人確認ではありません。

公開前に [Firebase ConsoleのAuthentication / Sign-in method](https://console.firebase.google.com/project/kotae-ai-u22-2026/authentication/providers) でGoogle providerを有効化し、サポートメールを選んで保存します。Authorized domainsには`kotae-ai.web.app`と`kotae-ai.firebaseapp.com`が必要です。未設定時はクライアントを`IDENTITY / REQUIRED`に保ち、サーバーも匿名・未検証tokenを拒否します。

## ローカル検証

```powershell
go test ./...
cargo test --workspace --locked
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
```

PDFの課題との対応と未解決点は [「Aと聞かれてAと答えられない」と実装の対応](docs/pdf-alignment.md)、クラウド構成と再配備手順は [Firebase / Google Cloud接続手順](docs/cloud-setup.md)、推論機構の設計と評価計画は [KOTAE Reflex設計](docs/kotae-reflex.md) に記録しています。

## セキュリティ原則

- Firebase ID token、Firebase App Check、`https://kotae-ai.web.app`と完全一致するOriginをすべて検証する
- `turnMode`を各turnで明示し、UID単位とFirebase App単位のquotaを本文デコード前に消費する
- サービスアカウントJSON鍵を作らず、Cloud Runの専用サービスIDを使う
- 原音、文字起こし、モデル応答、PDF本文、token、秘密鍵をKOTAEのアプリログへ出さない。クラウド事業者全体のゼロ保持とは表現しない
- STT / TTSは`asia-northeast1`のリージョナルエンドポイントへ固定する
- regional DLPの検査・置換が成功した文字列だけをVertex AIへ送り、DLPを完全なPII検出とは表現しない
- Vertex AIは`global`であり、検査・置換後の文字列が日本リージョン内に限定されるとは説明しない
- PDFはクライアントで選択・読込・送信を止め、APIへ直接送られてもモデル推論前に拒否する
- 状態鍵はSecret Managerで管理し、状態トークンはFirebase UIDへ束縛して15分で失効させる
- 音声履歴、再生履歴、無人の後日再評価、保存音声Vaultは現在の公開経路に実装していない
- 話者本人認証は実装していないため、同席者の声を自動採用せず、利用者が相手の質問を言い直した時だけ受け答え支援として扱う
- respondent coachingは利用者が前景で開始・継続した`intentional` / `foreground` turnだけで動かし、受動的な`ambient` turnから保留質問を作成・変更・進行・解除しない
- Crossref候補発見を「検証済み」と呼ばない。任意Web巡回、論文本文取得、claim-evidence照合、定期的な自動収集はまだ実装していない
