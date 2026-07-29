# アーキテクチャ

## 作品の核

コタエーAIは、固定質問に対する採点器ではありません。明示的に開始した音声セッションから潜在的な問いと話題の移行を推定し、「Aを聞かれているのにAへ答えていない」状態を検出し、意味を変えずに答えの核を前へ出せる時だけ介入します。

```text
発話
  ▼
潜在問いの仮説 ──→ Latent Answer Contract
  │                  ├─ 問いのoperatorと必須slot
  │                  ├─ 最初のcommitmentとtarget coverage
  │                  └─ 条件・不確実性を守るcounterfactual repair
  ▼
Revision-aware Thought State Graph
  ├─ goal / claim / evidence / constraint
  ├─ Self-repair grace
  └─ Expected Value of Intervention
       ├─ silence
       ├─ clarify
       └─ short spoken repair
```

KOTAE ReflexとLatent Answer Contract（LAC）はこのプロジェクトで設計した実験機構です。新規性はクラウドAPIを接続したことではなく、潜在問いの不確実性、答えの必須slot、冒頭のコミットメント、意味を保持した修復を一つの検証可能な契約として扱えるかに置きます。「世界初」や有効性が確立済みとは主張しません。

## 現在の実装経路

```text
┌──────────────────────────────────────────────┐
│ Browser                                      │
│ Rust / Dioxus / Wasm                         │
│  └─ JS bridge: MediaRecorder / Web Audio VAD │
│                 Firebase Auth / App Check    │
└───────────────────┬──────────────────────────┘
                    │ same-origin HTTPS
                    │ POST /api/v1/voice/turns
                    ▼
┌──────────────────────────────────────────────┐
│ Cloud Run / Go（asia-northeast1）             │
│ Auth + App Check + Origin + size + rate limit│
└──────────┬──────────────────┬────────────────┘
           │ raw audio        │ transcript / one-turn PDF
           ▼                  ▼
┌──────────────────┐   ┌─────────────────────────────┐
│ Cloud STT V2     │   │ Vertex AI（global）          │
│ asia-northeast1  │   │ Gemini fast / precision     │
│ chirp_3, ja-JP   │   │ Thought Graph + EVI + LAC   │
└────────┬─────────┘   └────────────┬────────────────┘
         └──── transcript ──────────┘
                                    │ silence / reply text
                                    ▼
                         ┌──────────────────────────┐
                         │ Cloud TTS                │
                         │ asia-northeast1          │
                         │ Chirp 3 HD, ja-JP        │
                         └────────────┬─────────────┘
                                      │ MP3
                                      ▼
                                   Browser
```

Cloud STTにはraw audioだけ、Vertex AIには文字起こしと任意のPDF、Cloud TTSには選択された短い応答文だけを渡します。音声、逐語録、PDF、応答文はアプリのDBやStorageへ保存しません。

## 状態

長い会話履歴をサーバーへ置かず、短い意味状態だけを暗号化tokenとしてブラウザメモリへ返します。

```text
AES-256-GCM token
  ├─ schema / issuedAt / expiresAt / turn
  ├─ 検出できたemail・電話番号らしい数列・token・高い原文重複を除いたshort Thought State Graph
  └─ last intervention metadata
```

tokenはFirebase UIDをAADへ含め、15分で失効します。鍵はSecret ManagerからCloud Runへ注入します。逐語録、会話・PDFの自由文要約、PDF本文、chain-of-thoughtは入れません。ただしフィルタ済みの意味nodeも機微情報になり得て、Cloud Runは復号できるためE2EEではありません。

## 技術境界

| 層 | 技術 | 責務 |
|---|---|---|
| 体験 | Rust / Dioxus / Wasm | 音声中心UI、session状態、アクセシビリティ |
| ブラウザ境界 | JavaScript module | MediaRecorder、Web Audio VAD、Firebase Web SDK、音声再生 |
| API | Go / Cloud Run | 認証、App Check、Origin、入力検証、timeout、rate limit |
| 音声認識 | Cloud Speech-to-Text V2 | `asia-northeast1`で日本語音声を文字へ変換 |
| 推論 | Go / Vertex AI | structured output、fast / precision routing、KOTAE Reflex |
| 答え契約 | Go | LACの決定論的検証、曖昧性・coverage・意味保存guard |
| 音声合成 | Cloud Text-to-Speech | `asia-northeast1`で短い応答文をMP3へ変換 |
| 一時状態 | Go / browser memory | UID-bound AES-GCM token、15分TTL |
| 運用メタデータ | Firestore | 評価メタデータとrate counterだけをTTL付きで保存 |

TypeScriptは使いません。ブラウザAPIとFirebase Web SDKを直接呼ぶ必要がある範囲だけをJavaScriptへ隔離し、認証判断、推論、暗号、LACをJavaScriptへ置きません。

## 推論経路

1. Gemini 3.6 Flashの高速経路が、domain、潜在問い、Thought State Graph差分、介入候補、advisory LACを構造化出力する。
2. PDF、研究・技術、高リスク領域、低信頼のturnはGemini 3.1 Pro previewの精密経路へ送る。PDF・医療・法律・金融・研究根拠では精密経路の失敗時に実質回答へfallbackしない。
3. 最終draftの後に、draft側のLACを入力しない独立structured callでLACを監査する。
4. モデル出力をJSON schemaと上限で検証し、Go側が仮説gap、entropy、必須slotの完全充足、回答内に実在するcommitment、意味保存を決定論的に再計算する。
5. 潜在問いが曖昧ならclarifyまたはsilence、答えの核が欠けていて意味保存できる時だけrestructureする。独立監査が使えない場合も未監査draftは話さない。
6. Self-repair graceとEVIで、モデルが話したがっても介入価値が低ければsilenceへ落とす。ただし緊急安全介入を曖昧判定で消さない。
7. 発話を選んだ場合だけ、短い応答文を東京リージョンTTSへ送り、同じ最終文だけをbounded captionとして返す。

LACの指標は内部評価用で、画面へ分析文を大量表示しません。モデルの非公開chain-of-thoughtを保存または表示する設計でもありません。

## 配信経路

- 静的なWasm UI: Firebase Hosting
- REST API: Firebase Hostingの`/api/**` rewriteからCloud Run `kotae-api`
- 一発話ごとの音声request: `POST /api/v1/voice/turns`
- Cloud Run、Speech-to-Text、Text-to-Speech: `asia-northeast1`
- Vertex AI: `global`

現在はWebSocketやVertex Live APIを使いません。一発話ごとのHTTPS requestに収め、処理中と音声再生中はマイクを無効にします。full-duplex、barge-in、保存音声履歴、Vault、無人の後日再評価は将来候補であり、現在の公開経路の保証には含めません。
