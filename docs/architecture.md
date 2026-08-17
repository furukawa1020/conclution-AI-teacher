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
│  └─ JS bridge: MediaRecorder / Web Audio VAD │
│                 Passkey Auth / App Check     │
└───────────────────┬──────────────────────────┘
                    │ authenticated WSS / HTTPS CORS
                    │ /api/v1/voice/live | turns:stream
                    ▼
┌──────────────────────────────────────────────┐
│ Cloud Run / Go（asia-northeast1）             │
│ Auth + App Check + Origin + size + rate limit│
└──────────────┬───────────────────────┘
               ├─ standard live
               │    raw PCM ──→ Vertex AI Native Audio（us-central1）
               │                ├─ final input caption gate後のPCM + caption
               │                ├─ 対象となる新規回答支援: Native出力破棄 → ローカルQ-ARC + private streaming TTS
               │                ├─ その他の初回回答支援: final captionを監査済み文字列plannerへ直接handoff
               │                └─ continuing answer coach: final captionを監査済み段階controllerへdonate
               │
               ├─ strict / connection fallback
               raw audio ──→ Cloud STT V2（asia-northeast1）
                                  ├─ strict: deterministic + regional DLP
                                  └─ Vertex AI（global）: fast / precision
                                       └─ reply text ──→ Cloud TTS（asia-northeast1）
               └─ runtime PDF upload: state decode・STT・modelより前に一律拒否
```

利用者のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した場合だけ、Cloud Runの独立tool-policy gateが許可します。自然文から検索同意を推測せず、追記、取消し、複数命令、ambient turnから外部queryは作りません。topicは「テーマは」と「の最新論文」の間全体、DOIは空白で区切ったbare DOI全体を決定論的に抽出し、モデル出力と取得結果へ完全に結びつけます。送信前にはNFKC差とUnicode format文字をfail-closedで拒否し、可逆encodingを再検査し、topic文字を限定し、topic内の節区切り・取消語とDOIに付いたcomma・semicolon・取消語も拒否します。固定hostのCrossref REST APIから返ったtitle、DOI、日付は候補発見にだけ使い、本文を読んだ証拠やclaimの支持根拠にはしません。topic探索は発表日ではなくCrossrefのindex date filterを使うため、「Crossrefの索引日が指定期間内の書誌候補」と表示します。任意の語が氏名か未知の技術名かを完全には区別できないため、固定発話のtopicそのものがCrossrefへ送られることもUIで明示します。

標準live turnはraw audioを`us-central1`のVertex AI Native Audioへ直接streamします。GA endpointのsetupは`responseModalities`を`AUDIO`一つだけに固定し、captionは`inputAudioTranscription` / `outputAudioTranscription`を有効化して受け取ります。`TEXT`は応答modalityへ併記しません。provider出力は最終入力captionが確定し、Cloud Run内の決定論的なPII・高リスク・tool要求screenを通るまで利用者へ解放しません。このscreenはregional DLPでも、Vertex AI送信前の原音検査でもありません。final input captionから明示回答支援を決定論的に確認した時はNative音声を破棄します。対象条件を満たす新規scopeでは、質問本文・回答候補・transcriptを入力に持たない質問拘束済みQ-ARCが有限template IDとslotを選び、監査済みrendererだけがopen-slot cueへ変換します。安定した暫定captionからdecisionとstreaming TTSをprivate commit buffer内で先行できますが、空白だけを正規化した候補byte列がfinal captionと完全一致し、browser commitが確定し、同じ報告質問由来の用途分離HMACへ束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointを発行した後だけPCMを解放します。汎用scope、汎用checkpoint、cached cueは使いません。不一致なら先読みした状態とPCMを破棄し、final captionを一度だけ処理します。それ以外はNative final captionを監査済み文字列plannerへ直接handoffし、同じ原音をCloud STTへ再送しません。回答支援の継続turnもNativeでfinal captionを確定し、そのcaptionを監査済み段階controllerへ直接donateします。継続コーチのために同じ原音を東京STTへ二重通過させません。実質問から作ったoperator、required slot、非可逆の質問継続tagで次turnを照合し、tokenにはcaption、外部質問本文、生成質問、回答候補を含めません。厳格モードとNative Audioを使えない接続fallbackだけがCloud STTへraw audioを渡す段階経路を使い、STTは一度だけです。厳格モードは文字起こしとモデル応答をCloud Run内の決定論的検査と東京リージョンのSensitive Data Protectionへ通し、両方が明示的に`clear`の時だけ`global`の文字列Vertex AIまたは東京リージョンのCloud TTSへ進めます。標準モードに同じ保証があるとは表示しません。runtime PDF uploadは標準・厳格の両モードで、state decode・STT・モデル推論より前にbackendが一律拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。提供PDFは課題・設計の参照資料であり、公開runtimeのupload機能ではありません。音声、逐語録、caption、応答文はアプリのDBやStorageへ保存しません。この構成では管理サービスが処理に必要な平文を扱うためE2EEではなく、DLPにも漏れがあり得るため完全なPII除去でもありません。

Native WebSocket handlerは認証、二段quota、UID leaseの後にpipelineを先行起動します。providerを使う通常Native turnでは、当該turnの`Open`が`SetupComplete`を受け取り、`StartActivity`が成功するまで`ready` frameもsocketからのPCM読取も開始しません。ブラウザ側も同じ`ready`を待ち、準備中はマイクtrackをmuteしたままcapture graph、VAD、`Listening`表示を開始しません。質問拘束済みstateの検査がproviderを開かない監査済みfallbackを選んだ場合だけ、state準備完了を入力受入境界とし、providerを開いたとは扱いません。開いたprovider接続は各turnの終了時に閉じるため、cross-turn pooling、session resumption、provider接続の再利用はありません。

AI応答へのbarge-inでは、既存sessionの端末内VAD、bounded MediaRecorder、対応端末の固定長PCM ringが割り込み候補を保持します。AEC確認済み・stateful drain不要のpre-final Native live・PCM handoffがそろう場合だけ、新しいprovider turnを裏で準備します。strong ready前に候補PCMを新providerへadopt・送信せず、発話終了後はNative readyを最大450 ms待って、間に合わなければHTTP fallbackへ切り替えます。それ以外は新providerを準備せずMediaRecorderのHTTP経路へ直ちに進みます。準備を行う経路では発話終了とroute確定をjoinし、到着順によらずforeground turnを一度だけ送信します。UIの「音声入力準備済み」は入力受入境界であり、監査済みno-provider fallbackのprovider接続を証明しません。AECを明示確認でき、Rustの強いguard閾値と複数change-pointを通った高確度候補だけは、端末内のclipped固定小数点逐次証拠で400〜520 msに確認できます。曖昧候補、AEC未確認・喪失、80 ms超のgap、sample clock異常はfast laneを閉じるだけで、既存720/1,200 ms判定を短縮しません。

開始操作を受理してから`Listening`までのprepare SLOは、会話本文を含まない`generation / latency_ms / outcome / result / route / version`だけで計測します。1,000 ms以下はon-target、1,000 ms超3,000 ms未満はslow、3,000 ms以上4,000 ms未満はmissed、4,000 ms以上はtimed-outです。Native live preflight自体には4,000 msのready timeoutを置き、captureとVADを開始する前にHTTP fallbackへ一方向に切り替えます。これは次段のspeech-endから有意味PCMまでの1秒目標・3秒miss・10秒stallと別のclockで、既存SLOの再試行・停止条件を変更しません。`GET /health`はprocess healthしか返さず、warmupしても次のturnのprovider setup、`StartActivity`、可聴開始は証明しません。起動時のcontent-free setup probeもrevision-levelの構成・IAM検査であり、各turnのstrong readyを置き換えません。

ブラウザの開始SLO controllerはturnごとの単調generationで所有権を固定し、route、経過時間、有限outcomeだけを扱います。最初の有意味PCMがWeb Audioの出力時刻へ配置された時だけclockを閉じ、無音PCM、WebSocket受信、視覚receipt、固定待ち音は成功に数えません。発火した3秒・10秒actionは各generation内でexactly-onceで、古いgenerationのcallbackは現turnを変更できません。Web Audioの推定可聴slotが発話終了+10秒より前かつ現在から250 ms以内ならoutput ownership時に先行確定し、それより遠いslotは推定可聴境界までactiveなままです。Nativeのsafe replayはspeech-end+3秒のmiss観測とは別にcommit+3秒より前には実行せず、未確定barge-in候補の解決を待ち、commit済み・audio event 0・Coach未発行・未割り込みの既存predicateを通る一回だけです。commit+3秒が発話終了+10秒より後ならmaster deadlineが先に停止します。このdeadlineはlive引継ぎ、capture/Blob確定、認証、HTTP、stream decode、validated playback drainを一つの所有権で覆います。有意味PCM 0ならHTTPをabortし、liveをfail-closeし、長い先頭無音の後へ予約されたWeb Audio sourceも停止します。無音audio eventや認証済みCoach checkpointは再送を禁止しますが10秒停止は無効化しません。音声や認証済みstateを公開済みのturnはdeadlineを理由に再実行しません。silent finalなどで先にterminal結果へ到達したturnはmaster deadlineを待たず既存のno-reply経路へ進みます。Cloud Run側の`commit_to_first_audio_ms`も、WebSocketへ正常に書けた最初の有意味PCMからだけ開始します。

標準Nativeのserver-side commit waterfallは、`commit_to_server_drain_ms`（commitからserver input drain）、`server_drain_to_activity_end_ms`（drainからproviderへの`ActivityEnd` write完了）、`activity_end_to_final_caption_ms`（`ActivityEnd`完了からeffective final-ready）、`final_to_risk_route_gate_ms`（effective final-readyから決定論的risk / route gate完了）、`output_commit_to_first_audio_ms`（`CommitOutput`からWebSocketへ公開できた最初の有意味PCM）の5区間を本文なしで別々に記録します。effective final-readyは`max(actual final caption, ActivityEnd write完了)`であり、field名から生のfinal caption時刻だけを終点とは解釈しません。このwaterfallはserver commit後だけを観測し、clientのspeech-end前の区間とspeech-endからserver commitまでを含まないため、各値やその組合せ自体はspeech-end起点の1秒SLO達成を意味しません。timeoutまたはcancelでも、そのrequestで既に観測した境界だけをrequest-local snapshotへ固定し、未観測区間は`-1`のままにして、snapshot後のlate callbackを無視します。5区間のlog fieldへaudio、caption、transcript、会話stateを入れません。

## 状態

標準モードでは長い会話履歴をサーバーへ永続化しません。通常のNative Audio turnは、決定論的な機微情報screenを通った直前一往復の末尾だけを上限化したAES-256-GCM認証暗号state leaseとしてブラウザメモリへ返し、次turnのprovider setupへdataとして渡します。screenが検出したturnは会話を失敗させずstate更新だけを省略します。DB read/writeと追加のモデル呼び出しはありません。回答支援はoperator、required slot、非可逆の質問継続tagと有限coach制御metadataを同じ認証暗号tokenとしてブラウザメモリへ返します。具体的な外部質問や回答本文は入れません。verifier-progress writerを有効にしたrolloutでは、現在の質問に対する検証の進行だけを表す5状態audit posteriorを合計10,000の固定小数massとして追加します。これは短期control priorであり、本人のretrieval状態、診断、能力、性格や内面の推定ではありません。厳格モードはcross-turn stateを受け取らず、返しません。

```text
AES-256-GCM token
  ├─ schema / issuedAt / expiresAt / turn
  ├─ 検出できたemail・電話番号らしい数列・token・高い原文重複を除いたshort Thought State Graph
  └─ last intervention metadata
```

tokenはFirebase UIDをAADへ含め、15分で失効します。鍵はSecret ManagerからCloud Runへ注入します。通常Nativeの直前一往復を除く長い逐語録、自由文要約、chain-of-thoughtは入れません。runtime PDF inputはtoken生成より前に拒否するため、PDF本文や要約も入りません。ただしフィルタ済みの意味nodeやscreenを通過した直前一往復も機微情報になり得て、Cloud Runは復号できるためE2EEではありません。

## 技術境界

| 層 | 技術 | 責務 |
|---|---|---|
| 体験 | Rust / Dioxus / Wasm | 音声中心UI、session状態、アクセシビリティ |
| ブラウザ境界 | JavaScript module | MediaRecorder、Web Audio VAD、Firebase Web SDK、音声再生 |
| API | Go / Cloud Run | 認証、App Check、Origin、入力検証、timeout、rate limit |
| 厳格PII境界 | Sensitive Data Protection / Go | 厳格モードの文字起こしと応答の検査、失敗時のモデル/TTS遮断 |
| 音声認識 | Cloud Speech-to-Text V2 | `asia-northeast1`で日本語音声を文字へ変換 |
| 推論 | Go / Vertex AI | structured output、fast / precision routing、KOTAE Reflex |
| 答え契約 | Go | LACの決定論的検証、曖昧性・coverage・意味保存guard |
| 本人回答gate | Go | exact evidence binding、意味節順序、否定・条件・数値・不確実性の不変条件 |
| 回答取出し制御 | Go | Q-ARCのanswer-free区間belief/minimax-regret、有限template/slotからclosed rendererへの境界、回答後verifier-progress audit posteriorのBayes更新と安全mask付き有限行動 |
| 研究候補探索 | Go / Crossref | 明示したDOI・新着topicの書誌候補。claim evidenceではない |
| 音声合成 | Cloud Text-to-Speech | `asia-northeast1`で短い応答文をMP3へ変換 |
| 一時状態 | Go / browser memory | UID-bound AES-GCM token、15分TTL |
| 運用メタデータ | Firestore | 評価メタデータ、rate counter、短命ceremony、live接続leaseをTTL付きで保存。Passkey public credentialはTTLなし |

TypeScriptは使いません。ブラウザAPIとFirebase Web SDKを直接呼ぶ必要がある範囲だけをJavaScriptへ隔離し、認証判断、推論、暗号、LACをJavaScriptへ置きません。

## 推論経路

1. 対象条件を満たす新規回答支援scopeは、モデルを呼ばないQ-ARC fastpathが有限template IDとslotを選び、別の監査済みclosed rendererだけがcueへ変換する。安定した暫定captionでdecisionとstreaming TTSをprivate commit buffer内に先行できるが、final captionの完全一致、browser commit、報告質問由来の非可逆tagへ束縛したAES-256-GCM認証暗号stateを含む有限checkpointがそろってからPCMを解放する。ここでは質問本文、答え候補、transcriptをQ-ARCへ渡さない。
2. それ以外ではGemini 3.6 Flashの高速経路が、domain、潜在問い、Thought State Graph差分、介入候補、advisory LACを構造化出力する。初回回答支援はNative final captionを直接受け取り、2回目のSTTを行わない。
3. 研究・技術、高リスク領域、低信頼のturnはGemini 3.1 Pro previewの精密経路へ送る。医療・法律・金融・研究根拠では精密経路の失敗時に実質回答へfallbackしない。runtime PDF uploadはモードにかかわらず、この経路へ入る前に拒否する。
4. 最終draftの後に、低遅延のfast modelを使う別のstructured callでLACを監査する。独立性は別モデルという主張ではなく、隔離prompt、別call、draft側LAC自己申告の非共有で確保する。高速監査が構造不正または一時的provider障害で二度失敗した時だけprecision modelの中思考で一度回復監査し、安全終了・cancelでは切り替えない。
5. モデル出力をJSON schemaと上限で検証し、Go側が仮説gap、entropy、必須slotの完全充足、回答内に実在するcommitment、意味保存を決定論的に再計算する。
6. `respondent`経路では、本人の回答試行、slotごとの完全一致evidence、protected spanをGoの別gateへ渡す。再構成案が元回答の意味節の並べ替えでない、または否定・条件・数値・不確実性・固有内容が変わった場合は拒否する。AIは再構成案を本人のAとして代読しない。Aが理由や前置きの後ろにある時は、本人が同じAを先に言えるよう一度だけ穏やかに再質問し、それでも成立しなければ通常会話へ解放する。二つのverifierを有限signalへ落とした後は、現在の質問に対する検証の進行だけを表す5状態verifier-progress audit posteriorをBayes更新し、content-free controllerが`wait / elicit / restate / complete / release`を選ぶ。本人のretrieval状態は推定しない。
7. 潜在問いが曖昧ならclarifyまたはsilence、答えの核が欠けていて意味保存できる時だけrestructureする。独立監査が使えない場合も未監査draftは話さない。
8. intentional turnで明示され、否定されていないDOI / 論文検索だけを、PII・credential screenの後にCrossrefへ送る。ambientでは常に拒否し、結果を`needs_primary_evidence`として現在のturnだけへ返す。任意URLは取得しない。
9. Self-repair graceとEVIで、モデルが話したがっても介入価値が低ければsilenceへ落とす。ただし緊急安全介入を曖昧判定で消さない。
10. 発話を選んだ場合だけ、短い応答文を東京リージョンTTSへ送り、同じ最終文だけをbounded captionとして返す。厳格モードはTTSより前に応答文もCloud Run内決定論検査 + regional DLPで`clear`と検証し、streaming音声を検証完了まで送信しない。

LACの指標は内部評価用で、画面へ分析文を大量表示しません。モデルの非公開chain-of-thoughtを保存または表示する設計でもありません。

## 配信経路

- 静的なWasm UI: Firebase Hosting
- REST API: Firebase Hostingの`/api/**` rewriteからCloud Run `kotae-api`
- 音声の主経路: 固定Cloud Run URLの`WSS /api/v1/voice/live`、検証済みfallback: 固定Cloud Run URLの`POST /api/v1/voice/turns:stream`
- Cloud Run、および段階的な経路のSpeech-to-Text、Text-to-Speech: `asia-northeast1`
- Vertex AI Native Audio: `us-central1`
- 文字列推論のVertex AI: `global`

通常の音声ターンは認証付きWebSocketで20 ms PCMを増分送信し、利用できない場合だけ同じ認証境界のHTTPS requestへ退避します。標準live会話はVertex AI Native Audioを使います。初回回答支援は質問拘束された有限型Q-ARC、または2回目のSTTを省いたcaption handoffを使います。継続CoachもNative final captionを監査済み段階controllerへ直接donateし、東京リージョンSTTへ同じ原音を再送しません。通常Native、初回回答支援、継続Coachを別系列にし、発話終了から最初の実質音声frameまで1,000 ms以内を運用SLOとして計測します。現行のroute metadataでは初回Q-ARCと初回caption handoffを互いに分離できないため、両者は初回回答支援へ合算します。視覚的な受領表示や無音`wait`は達成扱いにせず、ダミーの固定音声は生成も再生もしません。1秒は回線、端末、managed modelを含む絶対上限の保証ではありません。Native Audioの出力は既定384 token（設定可能範囲128〜512）で上限を固定し、短文指示と合わせてAIの長話を抑えます。クライアントcaptureは最大3分30秒、サーバーcaptureは最大4分または12,000 frame、turnは6分、Cloud Run requestは420秒で停止します。同じ仮名Firebase UIDのlive接続はFirestoreの短命leaseで同時に1本へ制限します。AI処理中と音声再生中は、利用者が開始したセッション内に限って端末内VADで訂正・割り込みを待ち、確認前PCMはAudioWorklet内の固定長リングから外へ出しません。明瞭なforeground音声は720 ms以上かつforeground density 0.72以上、静かな音声は1,200 ms以上、どちらも候補全体のvoice density 0.68以上を確認した時だけbarge-inとしてForeground turnへ引き継ぎ、短いぼやきや相づちは端末内候補として破棄します。AECを`true`と確認できない端末では、AI出力中の候補を720 ms未満で減衰せず、20 msで一時muteして120 msのspeaker tailを除外した後、さらに240 ms続いた本人音声だけを割り込みとして確定します。この半二重probeはWeb Audio timelineへ480 msで復帰を開始する予約を先に置き、AEC未確認の生PCMをlive WebSocketやAudioWorkletの送信ringへ渡しません。話者本人認証、保存音声履歴、Vault、任意Web巡回、論文本文のclaim-level検証、無人の後日再評価は現在の公開経路の保証には含めません。

1.6秒未満の短い明瞭な標準Native発話のendpointは、provider通知と端末VADの二重一致なら280 ms、端末単独のfallbackなら400 msです。1.6秒以上または検証済みのchanging-envelope継続証拠がある発話は2.2秒、静かな発話は3秒、長い独話は5秒、active Coachは既存の保護窓を維持します。ブラウザはNative AudioとHTTP fallback・厳格経路のすべてで、最初の有意味PCMをoutput timestampへ配置した時だけ、speech-endから推定可聴開始までのcontent-free値をcurrent-turn eventとしてWasmへ渡します。Nativeはfinal captionを受理したcommit境界、HTTPは現在turnを送信したcommit境界より後だけ公開します。Wasmは完全一致schema、version、0〜120秒の有限範囲を検証し、現在sessionだけに「返答開始 約N.N秒」を表示します。この値は端末出力時刻に基づく推定であり、保存、能力評価、意味理解の証明には使いません。
