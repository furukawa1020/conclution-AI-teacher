# アーキテクチャ

## 作品の核

コタエーAIは、固定質問に対する採点器ではありません。明示的に開始した音声セッションから潜在的な問いと話題の移行を推定します。さらに「KOTAE自身が答える質問」と「別の人から利用者へ向けられた質問」を区別し、後者では本人の回答試行に実在する意味だけで「A」を前へ出せる時だけ介入します。

```text
相手の質問 + 本人の回答試行
  ▼
潜在問いの仮説 ──→ Latent Answer Contract
  │                  ├─ 問いのoperatorと必須slot
  │                  ├─ 最初のcommitmentとtarget coverage
  │                  └─ 条件・不確実性を守るcounterfactual repair
  ▼
Respondent Meaning Gate
  ├─ 本人回答内のexact slot evidence
  ├─ 既存semantic clauseの並べ替えだけを許可
  └─ 否定・条件・数値・不確実性・固有内容をfail-closed
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
│  └─ JS bridge: AudioWorklet / Web Audio VAD  │
│                 Firebase Auth / App Check    │
└───────────────────┬──────────────────────────┘
                    │ authenticated WSS
                    │ 20 ms PCM /api/v1/voice/live
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
│ chirp_3, fixed   │   │ Thought Graph + EVI + LAC   │
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

利用者のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した場合だけ、Cloud Runの独立tool-policy gateが許可します。自然文から検索同意を推測せず、追記、取消し、複数命令、ambient turnから外部queryは作りません。topicは「テーマは」と「の最新論文」の間全体、DOIは空白で区切ったbare DOI全体を決定論的に抽出し、モデル出力と取得結果へ完全に結びつけます。送信前にはNFKC差とUnicode format文字をfail-closedで拒否し、可逆encodingを再検査し、topic文字を限定し、topic内の節区切り・取消語とDOIに付いたcomma・semicolon・取消語も拒否します。固定hostのCrossref REST APIから返ったtitle、DOI、日付は候補発見にだけ使い、本文を読んだ証拠やclaimの支持根拠にはしません。topic探索は発表日ではなくCrossrefのindex date filterを使うため、「Crossrefの索引日が指定期間内の書誌候補」と表示します。任意の語が氏名か未知の技術名かを完全には区別できないため、固定発話のtopicそのものがCrossrefへ送られることもUIで明示します。

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
| 本人回答gate | Go | exact evidence binding、意味節順序、否定・条件・数値・不確実性の不変条件 |
| 研究候補探索 | Go / Crossref | 明示したDOI・新着topicの書誌候補。claim evidenceではない |
| 音声合成 | Cloud Text-to-Speech | `asia-northeast1`で短い応答文をMP3へ変換 |
| 一時状態 | Go / browser memory | UID-bound AES-GCM token、15分TTL |
| 運用メタデータ | Firestore | 評価メタデータとrate counterだけをTTL付きで保存 |

TypeScriptは使いません。ブラウザAPIとFirebase Web SDKを直接呼ぶ必要がある範囲だけをJavaScriptへ隔離し、認証判断、推論、暗号、LACをJavaScriptへ置きません。

## 推論経路

1. Gemini 3.6 Flashの高速経路が、domain、潜在問い、Thought State Graph差分、介入候補、advisory LACを構造化出力する。
2. PDF、研究・技術、高リスク領域、低信頼のturnはGemini 3.1 Pro previewの精密経路へ送る。PDF・医療・法律・金融・研究根拠では精密経路の失敗時に実質回答へfallbackしない。
3. 最終draftの後に、低遅延のfast modelを使う別のstructured callでLACを監査する。独立性は別モデルという主張ではなく、隔離prompt、別call、draft側LAC自己申告の非共有で確保する。高速監査が構造不正または一時的provider障害で二度失敗した時だけprecision modelの中思考で一度回復監査し、安全終了・cancelでは切り替えない。
4. モデル出力をJSON schemaと上限で検証し、Go側が仮説gap、entropy、必須slotの完全充足、回答内に実在するcommitment、意味保存を決定論的に再計算する。
5. `respondent`経路では、本人の回答試行、slotごとの完全一致evidence、protected spanをGoの別gateへ渡す。再構成案が元回答の意味節の並べ替えでない、または否定・条件・数値・不確実性・固有内容が変わった場合は拒否する。
6. 潜在問いが曖昧ならclarifyまたはsilence、答えの核が欠けていて意味保存できる時だけrestructureする。独立監査が使えない場合も未監査draftは話さない。
7. intentional turnで明示され、否定されていないDOI / 論文検索だけを、PII・credential screenの後にCrossrefへ送る。ambientでは常に拒否し、結果を`needs_primary_evidence`として現在のturnだけへ返す。任意URLは取得しない。
8. Self-repair graceとEVIで、モデルが話したがっても介入価値が低ければsilenceへ落とす。ただし緊急安全介入を曖昧判定で消さない。
9. 発話を選んだ場合だけ、短い応答文を東京リージョンTTSへ送り、同じ最終文だけをbounded captionとして返す。

LACの指標は内部評価用で、画面へ分析文を大量表示しません。モデルの非公開chain-of-thoughtを保存または表示する設計でもありません。

## 配信経路

- 静的なWasm UI: Firebase Hosting
- REST API: Firebase Hostingの`/api/**` rewriteからCloud Run `kotae-api`
- 通常の音声経路: Cloud Runへの認証済み`WSS /api/v1/voice/live`
- 互換fallback: `POST /api/v1/voice/turns`
- Cloud Run、Speech-to-Text、Text-to-Speech: `asia-northeast1`
- Vertex AI: `global`

通常経路は、Firebase ID tokenとApp Check tokenをURLへ入れず最初のWebSocket frameで検証してから、20 msのPCMをCloud Runへ送ります。Cloud STTのendpoint通知と端末VADの無音判定が一致した時だけ早期commitし、どちらか片方だけでは推論開始を確定しません。安定した途中認識では同じGemini推論とTTSを先読みしますが、合成音声は最大24,000 byte（500 ms）をcommit bufferへ隔離し、最終文字起こし・監査済み応答との一致が確定した時だけ解放します。不一致、外部操作、cancel、形式不正ではbufferをzeroizeして破棄します。応答再生中もエコー除去付きVADで利用者の声を確認でき、割り込み確定時には再生中・待機中の音声を破棄して次ターンへ100 msのpre-rollを引き継ぎます。Vertex Live APIは使わず、話者本人認証、保存音声履歴、Vault、任意Web巡回、論文本文のclaim-level検証、無人の後日再評価は現在の公開経路の保証に含めません。
