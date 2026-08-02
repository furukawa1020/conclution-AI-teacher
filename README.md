# コタエーAI

コタエーAIは、本人の答えを代作したり、採点して一律に矯正したりするアプリではありません。相手から聞かれた質問と、まとまらないまま話した本人の回答を分けて捉え、「A」を本人自身の言葉で先に返せるよう、普通の音声会話の中で必要な時だけ短く支えます。通常の質問、日常のぼやき、考え途中の独り言、研究や論文の話にも応じ、本人の言い直しで解決しそうな時は割り込みません。会話を課題や訓練にせず、短い一往復だけでも終えられます。この会話支援が回答能力を長期に向上させることはまだ実証していません。

公開版: [https://kotae-ai.web.app](https://kotae-ai.web.app)

## 作品の核

- **KOTAE Reflex**: 発話から目標、主張、根拠、制約を短いThought State Graphとして更新し、自己修正を待つ時間とExpected Value of Intervention（EVI）を使って、話すか沈黙するかを決める
- **Latent Answer Contract（LAC）**: 潜在的な問いを最大3候補まで仮説化し、問いが要求する型と回答冒頭のコミットメントを照合する。問いが曖昧なら決めつけず、答えの核が後ろへ埋もれた時だけ、条件と不確実性を変えない再構成を許可する
- **Respondent Coach + Meaning Gate**: 「AIへの質問」と「相手から本人へ向けられた質問」を区別する。後者では本人の回答内に完全一致するslot evidenceを束縛して判定するが、AIの再構成案を本人の答えとして読み上げない。Aが後ろなら固定文で一度だけやさしく聞き直し、次は普通の会話へ戻る。Aを先に言えたら通常はそこで閉じ、本人が同じ支援中に厳密句「理由まで一問お願いします」と明示した時だけ、理由・根拠・最初の一歩のうち質問の型に合う一問を足す。この任意の一問は二段目の合格試験にせず、最初の実質的な返答で閉じる。考え中の「えっと」「うーん」は失敗回数にせず、無監査のモデル文ではなく固定の短い相づちを返す
- **Research discovery**: 本人のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した時だけ、固定語の間にあるtopicまたはbare DOIをCrossrefへ最小送信する。返すのはCrossrefの索引日が指定期間内の書誌候補であり、論文の発表日順でも、本文や主張を検証済みとした結果でもない
- **音声向け出力**: 内部の分析を画面へ大量表示せず、必要な一つの介入だけを短い日本語音声へ変換する
- **端末内の長期測定**: 明示的に参加した人だけを対象に、固定された未見質問への自己評価をbaseline・4週・8週・追跡時点で端末内へ記録し、2時点以上では有限項目の生値だけを表示する。会話本文や音声は記録せず、撤回、競合時の停止、削除を実装しているが、比較試験でも改変耐性のある研究台帳でもなく、長期効果は未実証

KOTAE ReflexとLACは、このプロジェクトで設計・実装している実験的な仕組みです。近接研究を踏まえていますが、「世界初」や有効性が確立済みとは表現しません。新規性の評価は、既存APIの組み合わせではなく、潜在問いの同定、答えの先頭契約、意味保存を伴う修復が実測で機能するかによって行います。

## 現在の音声経路

```text
Rust / Dioxus / Wasm UI
  └─ ブラウザJavaScript境界: MediaRecorder、VAD、Firebase SDK
       └─ Firebase Hosting /api rewrite または固定Cloud Run URLへ認証付きTLS
            └─ WebSocket、HTTPS stream、またはPOST /api/v1/voice/turns
                 └─ Cloud Run / Go
                  ├─ 標準live・PDFなし: raw PCM ──→ Vertex AI Native Audio（global）──→ PCM + caption
                  └─ 厳格 / PDF / 接続fallback
                       ├─ Cloud Speech-to-Text V2（asia-northeast1）
                       ├─ 厳格モード: Cloud Run内の決定論的検査 + Sensitive Data Protection（asia-northeast1）
                       ├─ 文字列 ──→ Vertex AI（global）: KOTAE Reflex + LAC
                       ├─ Crossref（明示したDOI / 新着論文検索だけ）
                       └─ Cloud Text-to-Speech（asia-northeast1）
```

マイクは利用者が明示的に開始したセッション中だけ使います。端末側VADが一つの発話を区切り、認証済みのWebSocketを優先し、使えない時だけ認証済みHTTPSへ退避します。低遅延streamとWebSocketは固定したCloud Run URLへ直接CORS/TLSで接続し、同じ仮名アカウントのlive接続はFirestoreの短命leaseで同時に1本へ制限します。長い独話はクライアント最大3分30秒、サーバー最大4分で安全に区切り、Cloud Runの420秒timeoutより内側で終了します。最後の声から700 ms無音になった時点で、内容を理解したとは主張しない「ここまで届いています」を端末上に表示し、発話再開時は即座に消します。標準liveの短い明瞭な発話はNative Audioで段階的なSTT・推論・TTS待ちを避け、1秒で話し始めることを目標にしますが、回線とmanaged modelを含む絶対1秒保証ではありません。

標準liveでPDFを添付しないturnは、raw audioをCloud Runから`global`のVertex AI Native Audioへstreamし、音声とcaptionを受け取ります。最終入力captionの確定前には生成音声を解放せず、Cloud Run内の決定論的なPII・高リスク・tool要求screenを通過した時だけ利用者へ送ります。このscreenはregional DLPでも、Vertex AIへ送信する前の原音検査でもありません。厳格モード、PDF turn、Native Audioを使えない接続fallbackは東京リージョンSTT、文字列のVertex AI推論、東京リージョンTTSの段階的な経路を使います。厳格モードは別のrequest型として束縛し、文字起こしと応答文の両方がCloud Run内の決定論的検査とregional DLPで`clear`になった時だけ後段へ進め、PDF、外部検索、cross-turn stateを許可しません。どちらもE2EEでも完全なPII除去でもありません。

原音、文字起こし、Native Audioのcaption、モデル応答、PDF本文、研究query・候補はKOTAEのFirestore、Cloud Storage、アプリログへ保存しません。これはクラウド事業者全体の絶対的なゼロ保持保証ではありません。Native Audio turnが返す状態tokenは発話本文を含まず、段階的な標準経路の会話状態も自由文要約を避け、短い意味nodeと制御メタデータだけをAES-256-GCMで暗号化してブラウザメモリへ返します。ただし、後者には未検出の機微情報が残る可能性があり、Cloud Runは復号できます。厳格モードでは会話状態自体を返しません。正確な境界は [音声セキュリティ設計](docs/audio-security.md) を参照してください。

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
internal/privacyguard    Cloud Run内の決定論的検査 + regional DLPのfail-closed境界
internal/speechio        東京リージョンのSTT / TTS境界
internal/voiceflow       音声認識、推論、音声合成の一時処理
docs                     クラウド、セキュリティ、研究設計
```

TypeScriptは使いません。JavaScriptは、ブラウザから直接必要なMediaRecorder、Web Audio、Firebase Web SDKをRust/Wasmへ橋渡しする範囲に限定します。認証、入力検証、推論、状態暗号化、LACはGoまたはRust側です。

## Passkeyによるアカウント操作確認

初回は専用操作でPasskeyを登録し、以後の音声開始前にWebAuthnのuser verificationを伴う署名を検証します。秘密鍵はPasskey providerが管理し、KOTAEのブラウザコードとサーバーは受け取りません。同期や保管の方式は利用するOS・ブラウザ・Passkey providerに依存します。サーバーは検証済みceremonyから仮名Firebase account用custom tokenを発行します。ceremonyはApp Check、RP ID、exact origin、5分期限、単回利用へ束縛します。Firestoreのdocument IDはcredential ID等のSHA-256から作り、本文には検証に必要な仮名user handle、public credential、sign counterを保存します。秘密鍵は保存しません。

これは「登録済みauthenticatorを使って仮名アカウントを操作した」ことの確認です。法的身元、戸籍上の本人、端末の唯一の所有者、現在マイクで話す人を証明しません。声紋は収集しません。アカウントの回復、credential追加・失効、削除UIも未実装です。

Passkey ceremony APIにはFirebase App Checkを必須とします。既存Firebase sessionは`browserSessionPersistence`へ限定し、署名検証時刻を固定したcustom claim `kotae_passkey_at`を検証します。custom tokenを後から交換してもこの時刻は更新されないため、音声の5分境界を延命できません。本番では音声全体をPasskey必須にするflagが未指定でもsecure defaultで有効になり、buffered、streaming、WebSocketの全経路に同じ境界を適用します。

## ローカル検証

```powershell
go test ./...
cargo test --workspace --locked
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
```

PDFの課題との対応と未解決点は [「Aと聞かれてAと答えられない」と実装の対応](docs/pdf-alignment.md)、クラウド構成と再配備手順は [Firebase / Google Cloud接続手順](docs/cloud-setup.md)、推論機構の設計と評価計画は [KOTAE Reflex設計](docs/kotae-reflex.md) に記録しています。

## セキュリティ原則

- Passkey検証から発行したFirebase ID token、Firebase App Check、`https://kotae-ai.web.app`と完全一致するOriginをすべて検証する
- `turnMode`を各turnで明示し、UID単位とFirebase App単位のquotaを本文デコード前に消費する
- サービスアカウントJSON鍵を作らず、Cloud Runの専用サービスIDを使う
- 原音、文字起こし、モデル応答、PDF本文、token、秘密鍵をKOTAEのアプリログへ出さない。Google Cloud全体の絶対的なゼロ保持とは表現しない
- 厳格 / PDF / Native Audioを使えないfallbackのSTT / TTSは`asia-northeast1`のリージョナルエンドポイントへ固定する
- 厳格モードはSTT文字列と応答文のCloud Run内決定論検査 + regional DLP検査が`clear`の時だけ後段へ進め、失敗時は停止する。標準モードに同じ保証があるとは表現しない
- Vertex AIは`global`であり、標準liveのraw audio、評価APIで置換した文字列、厳格音声で検査済みの文字列や応答が日本リージョン内に限定されるとは説明しない
- 標準モードのPDFは利用者が選んだ次の一turnだけVertex AIへ渡し、応答後に参照を解放する。厳格モードでは選択・読込・送信を止め、APIでも拒否する
- 状態鍵はSecret Managerで管理し、状態トークンはFirebase UIDへ束縛して15分で失効させる
- 音声履歴、再生履歴、無人の後日再評価、保存音声Vaultは現在の公開経路に実装していない
- Passkeyによるアカウント操作確認は実装したが、話者本人認証ではない。現行VADは同席者・テレビ・合成音声を利用者の声から識別できないため、周囲の声を取り込まない環境で、利用者自身が相手の質問を言い直した時だけ使う
- 端末内の任意長期測定と時点別の生観測表示は実装済みだが、個人内の自己記録であって有効性・因果効果を示す比較試験ではない
- respondent coachingは利用者が前景で開始・継続した`intentional` / `foreground` turnだけで動かし、受動的な`ambient` turnから保留質問を作成・変更・進行・解除しない
- Crossref候補発見を「検証済み」と呼ばない。任意Web巡回、論文本文取得、claim-evidence照合、定期的な自動収集はまだ実装していない
