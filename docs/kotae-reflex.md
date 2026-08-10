# KOTAE Reflex 設計

## 結論

KOTAE Reflexは、固定質問を表示して採点する音声チャットではありません。日常のぼやき、独り言、考え途中の発話、研究の議論には、まず普通の会話相手として内容へ返します。まとまらない発話、長い沈黙、フィラー、小さな声を失敗とみなしません。

答え方を支える`respondent`経路は、次の条件をすべて満たす時だけ一問に限って有効にします。

1. 利用者自身が、その場で答え方の支援を明示的に頼んでいる。
2. 「今日は話すだけ」で支援停止を指定していない。
3. 何を答える質問かを一つに安全に絞れ、本人の自己修正を待ってもなお小さな手掛かりに便益がある。
4. 介入の便益が、誤訂正、集中阻害、心理的圧力、プライバシーの損失を上回る。

普通の雑談を採点、言い直し課題、隠れた練習へ変えません。「今日は話すだけ」は現在の支援を直ちに解除し、本人が新しく頼むまで通常会話を続ける指示です。「沈黙」も失敗や機能制限ではなく、重要な出力の一つです。

中心に置く研究仮説は、既存部品の統合そのものではなく、**Latent Answer Contract（LAC）**です。LACは「何を聞かれているか」を一つに決め打ちせず、問いの型と上位仮説、答えに必要なslot、最初のcommitmentを明示します。そのうえで、答えの核が抜けた、または後ろへ埋もれた時だけ、元の条件と不確実性を変えないcounterfactual repairを許可します。

LACを次の制御と組み合わせ、誤った訂正より沈黙を優先します。

- 後から解釈を訂正できるRevision-aware Thought State Graph
- 利用者自身の言い直しを優先するSelf-repair grace
- 沈黙との比較で発話を選ぶExpected Value of Intervention
- 深い推論と短い音声表現を分けるThink-Verbalize-Speak
- 音声転送とsessionを制限する端末側Privacy Sentinel

これらは、このプロジェクトで設計・実装している実験的な機構です。近接研究は存在し、有効性や新規性の査読評価はまだ受けていません。「世界初」とは断定せず、LACの潜在問い同定、target slot coverage、commitment位置、meaning preservationが既存baselineより改善するかを実験で評価します。2026年7月時点で、日本語の小声・沈黙を含む日常音声会話から結論先行技能が長期定着することを直接示した試験は確認できていません。この製品を診断、治療、性格やひきこもり状態を変える介入とは表示せず、苦手な場面を本人に知らせず練習させません。

低圧な会話方針は、厚生労働省の[ひきこもり支援ハンドブック](https://www.mhlw.go.jp/content/12200000/001605332.pdf)にある、何気ない会話、答えやすい問い、本人のペースと自己決定を尊重する原則を参照しています。会話練習への短いフィードバックは[IMBUEのランダム化研究](https://aclanthology.org/2024.acl-long.47/)に近接根拠がありますが、英語テキストの単回練習であり、本製品の対象、音声、長期効果を直接検証したものではありません。

本資料には、現在動く増分WebSocket、Vertex AI Native Audio高速経路、同期圧縮HTTP fallbackを持つターン型MVPと、将来研究するfull-duplex構成の両方を記載します。現在実装済みの境界は次節と「実装段階」を正とし、それ以外のイベント、session resumption、検索、個人適応は設計目標です。

約3分の独話を一つのturnとして受ける経路は実装済みです。ただし、これは「3分話せば技能が改善する」という効果の実装ではありません。現在実装しているのは、長い発話を途中で急いで切らず、最終文字起こしに十分な意味内容がある時に限って、同じturn内で中心点を意味保存して先に返す足場です。長期定着、他場面への転移、心理的負荷の軽減は未実証です。

## 対象範囲と非目標

対象範囲は次の三つです。

| モード | 例 | 主な介入 |
|---|---|---|
| 日常 | 予定、仕事、人間関係、意思決定のぼやき | 目標の反射、見落とした制約、次の一歩 |
| 独り言 | 考えながら話す、案を比較する、行き詰まる | 自己修正を待つ、ループ検知、短い再構成 |
| 研究 | 論文、技術調査、仮説、実験結果の議論 | 主張と根拠の対応、反証、引用付き確認 |

次は非目標です。

- 明示的に開始していない時間の常時盗聴
- 普通の雑談を採点、言い直し課題、隠れた練習へ変えること
- 文法、方言、フィラー、話し方の一律な矯正
- 声から性格、病気、精神状態を診断すること
- 本人が頼んでいない苦手な場面を練習させたり、診断・治療を行ったりすること
- 高リスクな医療、法律、金融判断を音声だけで確定すること
- 本人確認や重要操作の承認に声紋だけを使うこと
- 利用者の確認なしにメール送信、購入、公開、削除を実行すること
- 添付されていない論文や端末内ファイルを読んだふりをすること

## 現在の全体構成

```text
明示的に開始したブラウザsession
  │ Rust / Dioxus / Wasm UI
  │ JavaScript境界: MediaRecorder + Web Audio VAD
  ▼
固定run.app WebSocket / HTTPS fallback
  │ Passkey由来Firebase Auth + App Check + exact Hosting Origin
  ▼
Cloud Run / Go（asia-northeast1）
  ├─ 標準live: raw PCM → Vertex AI Native Audio（us-central1）
  │    ├─ 通常: gate後のPCM + caption → ブラウザ再生
  │    ├─ 対象となる新規回答支援: Native出力破棄 → ローカルQ-ARC → private streaming TTS
  │    ├─ その他の初回回答支援: Native final caption → 監査済み文字列planner（2回目のSTTなし）
  │    └─ 継続回答支援: Native final caption → 監査済み段階controllerへdonate（東京STT二重通過なし）
  ├─ 厳格 / 接続fallback: raw audio → Cloud STT V2（asia-northeast1）
       ├─ transcript → 厳格時だけCloud Run内決定論的検査 + DLP（asia-northeast1）
       ├─ clearまたは標準transcript → 文字列Vertex AI（global）: Thought State Graph / LAC / EVI
       └─ 選ばれた短い応答文 → 厳格時だけCloud Run内決定論的検査 + DLP → Cloud TTS（asia-northeast1）
                                                                                     └─ MP3 → ブラウザ再生
  └─ runtime PDF upload → state decode・STT・modelより前に全モードで拒否
```

raw audio、文字起こし、モデル応答はアプリ側で永続化しません。runtime PDF uploadは全モードでstate decode・STT・モデル推論前にbackendが拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。提供PDFは課題・設計の参照資料であり、公開runtimeで利用できる添付機能ではありません。標準モードで本人が回答支援を明示した初回turnは、`us-central1`のNative Audioが返したfinal input captionをCloud Runの決定論的な同意境界で検査し、Native出力を破棄します。対象条件を満たす新規scopeでは、質問本文・回答本文を入力に取らない質問拘束済みQ-ARCが有限template IDとslotを選び、監査済みrendererだけがcueへ変換します。安定した暫定captionからdecisionとstreaming TTSをprivate commit buffer内で先行できます。空白だけを正規化した候補byte列がfinal captionと完全一致し、browser commitが確定し、同じ報告質問由来の用途分離HMACへ束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointを発行するまでstate、判断、PCMを解放しません。汎用scope、汎用checkpoint、cached cueは使わず、不一致では先読みした状態とPCMを破棄し、final captionを一度だけ処理します。それ以外はfinal input captionを監査済み文字列plannerへ直接渡します。保留中の継続turnもNativeでfinal captionを確定し、そのcaptionを監査済み段階controllerへ直接donateします。初回・継続とも同じ原音を東京リージョンSTTへ再送しません。段階plannerは実際の外部質問からoperator、required slot、非可逆の質問継続tagを作り、次の無関係な発話を回答完了にしません。短期stateには逐語録、外部質問本文、生成質問、回答候補、自由文要約を入れません。段階的な標準turnのcross-turn stateにも逐語録・発話本文、モデル応答本文、`extended_speech`の今回限りの判定値、自由文要約を入れません。一方、長い発話も通常会話と同じ状態更新の対象であり、Cloud Runの決定論的規則で検査した、抽象化済みで件数・長さに上限のあるgoal、claim、ground、assumption、constraint、open loop、contradiction、decisionと制御メタデータは残り得ます。これらだけをAES-256-GCMで認証暗号化したUID-bound tokenとしてブラウザメモリへ返し、15分で失効させます。氏名など未検出の機微情報がnodeへ残る可能性はあります。厳格モードは文字起こしと応答文がCloud Run内の決定論的検査とregional DLPの両方で`clear`の時だけ後段へ進み、cross-turn stateを返しません。Native Audioは`us-central1`、文字列Vertex AIは`global`なので、raw audioや段階経路で渡した文字列が東京リージョンだけに留まるとは保証しません。どちらもE2EEでも完全なPII除去でもありません。

会話の足場を調整する状態は、意味nodeと分けます。保持するのは、通常会話だけにする選択、`guided / light / natural`の段階、質問のcooldown、明示支援turnで質問に対応するevidenceを受け取った直近回数という短期の列挙・上限付きメタデータだけです。発話本文、答え、話題、性格・健康推定、能力scoreを入れず、15分のTTLまたはセッション終了で破棄します。この内部状態から点数、連続成功、順位をUIへ表示しません。

### 現在のMVP境界

ブラウザだけで高精度な日本語ASR、意味推論、自然なfull-duplex音声をすべて端末内処理するのは、対応端末、電力、モデル配布量の面でまだ不安定です。そのため現在のMVPは次の境界です。

- 利用者が最初にタップして開始したsession中だけマイクを使う。
- 端末VADで一発話を区切り、録音開始から最大3分30秒まで認証付きWebSocketでCloud Runへ20 ms PCMを増分送信する。標準live turnはまず`us-central1`のVertex AI Native Audioへ中継する。本人自身が回答支援を明示したことをfinal入力captionから検証した場合はNative出力を破棄し、対象条件を満たす新規scopeならローカルQ-ARC、それ以外はcaption直接handoffを使う。保留中の継続回答もNative final captionを監査済み段階controllerへdonateし、初回・継続とも同じ原音へ2回目のSTTを行わない。厳格モードとlive接続fallbackだけがraw audioから段階経路を使う。runtime PDF uploadは全モードでこの経路へ入る前に拒否する。発話が確定しない無音候補は最大30秒で終了するため、その上限直前から話し始めても約3分は残るが、3分30秒の実発話を保証するものではない。Cloud Run側は最大4分、12,000 frame・7,680,000 byteまで受ける。Goのlive接続deadlineは6分、Cloud Runのrequest timeoutは420秒で、4分のcapture後にもcommitとモデル処理の時間を残す。
- WebSocket接続が利用不能な時、または高リスク・tool要求を検出してreview済みのNative zero-output sentinelを受けた時は、同じ認証境界のHTTPS requestへ一度だけ退避する。明示回答支援はQ-ARCまたはcaption handoffでlive接続内に留め、handoffがcheckpoint/音声解放前に失敗した場合だけfail-closed fallbackを許す。圧縮音声fallbackは2 MiB上限であり、codecとbitrateがブラウザごとに異なるため約3分を保証しない。2 MiBを超えたfallback chunkは全て破棄し、先頭だけの不完全な音声をuploadしない。live PCMが継続している場合はfallback超過だけを理由に発話を止めない。
- live WebSocketは音声受信前にFirestoreの短命leaseを取得し、同じ仮名Firebase UIDの同時接続を1本へ制限する。
- 標準Native WebSocketの`ready`は認証・quota・UID leaseだけでは送らない。そのturnでproviderを使う場合は`SetupComplete`受信と`StartActivity`成功まで待ち、ブラウザも準備中はマイクtrackをmuteして、クラウドPCM capture、VAD、`Listening`表示を開始しない。質問拘束済みstateがprovider不要の監査済みfallbackを選んだ場合だけ、state準備完了を入力受入境界とする。
- AI応答へのbarge-in候補は開始済みsessionの端末内VAD、bounded MediaRecorder、対応端末の固定長PCM ringで確認する。AEC確認済み・stateful drain不要のpre-final Native live・PCM handoffがそろう時だけ新providerを裏で準備し、strong ready前には候補PCMをadopt・送信しない。発話終了後にNative readyを最大450 ms待ち、間に合わなければHTTP fallbackへ切り替え、両方をjoinしてforeground turnを一度だけ送信する。それ以外は新providerを準備せずMediaRecorderのHTTP経路へ直ちに進む。
- raw audio、文字起こし、prompt/responseをKOTAEのDB、Storage、ログへ保存しない。runtime PDFはstate decode・STT・modelより前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する。
- 標準の高速会話モードではraw audioを`us-central1`のVertex AI Native Audioへ送り、同じprovider turnのfinal入力字幕が届き、Cloud Runの決定論的risk gateを通るまで生成音声を上限付きで保持する。GA endpointのsetupは`responseModalities`を`AUDIO`一つだけにし、captionは`inputAudioTranscription` / `outputAudioTranscription`で受け取る。`TEXT`は応答modalityへ併記しない。厳格モードはraw audioをVertex AIへ送らず、STT文字列と応答文がCloud Run内の決定論的検査と東京リージョンDLPの両方で`clear`の時だけ`global`の文字列推論または後段へ進む。標準モードに厳格モードと同じPII除去保証があるとは表示しない。
- Native Audio経路は発話の中心に関係する言葉から短く返すよう固定promptで制約する。段階経路ではcommit後のfinal transcriptが160 Unicode code point以上の時だけ、サーバーが今回限りの`extended_speech`を導出する。どちらも話した時間、能力、心理状態、習熟度の判定には使わない。
- 最後のvoiced frameから700 ms無音になった時は、内容非依存の固定表示「ここまで届いています」を出す。発話再開で消し、理解・要約・採点とは扱わない。これは視覚的な受領応答budgetであり、ダミーの固定音声は生成も再生もしない。意味音声の1秒保証ではなく、急ぐ場合は「ここで返して」で長い終端待ちを明示的に終えられる。
- 厳格モード、fallback、または初回・継続caption handoffの文字列planner/controllerで応答を選んだ時、短い文字列を東京リージョンTTSへ送る。対象条件を満たす初回Q-ARCの有限template ID/slotも別の監査済みclosed rendererでcueへ変換してstreaming TTSで合成し、安定した暫定captionではprivate commit buffer内だけで先行できる。通常の標準live turnはNative Audioの24 kHz PCMを返す。
- 標準live turnはVertex AI Native Audioを使うが、provider session resumptionや複数browser turn間のprovider session再利用はしない。AI応答へのbarge-inは、端末内VADで明瞭なforeground音声が720 ms以上かつforeground density 0.72以上、または静かな音声が1,200 ms以上続き、いずれも候補全体のvoice density 0.68以上となった時だけForeground turnへ引き継ぐ。短い相づちやぼやきでは停止しない。AECが確認できない端末ではAI出力中の720 ms候補をそのまま確定せず、20 ms mute後のspeaker tail 120 msを捨て、追加240 msの本人音声を確認する半二重probeを使う。gainはmute前に自動復帰を予約し、AEC未確認PCMをlive送信ringへ入れない。
- Respondent Coachの`awaiting_answer`、`awaiting_restatement`、`expanding`、`blocked/retry`中は、サーバーが認証した有限stateを権限の正本とする。継続turnもNative Audioでfinal captionを確定し、そのcaptionを監査済み段階controllerへ直接donateするため、同じ原音を東京リージョンSTTへ二重通過させない。本人の回答後は二つのverifierの有限signalから、現在の質問に対する検証の進行だけを表す5状態audit posteriorをBayes更新し、内容を生成しない制御器が`wait / elicit / restate / complete / release`を選ぶ。これは本人のretrieval状態や能力を推定するposteriorではない。`complete`と`blocked/release`後に始まる次turnから通常のNative応答へ戻す。`complete`のUIは「自分の言葉を声に出せた」という今回限りのreceiptであり、A-first達成、技能向上、他場面への転移を表示しない。
- 通常Native会話、初回回答支援、継続Coachを別系列にし、発話終了から最初の実質音声frameまで1,000 ms以内を運用SLOとして集計する。現行のroute metadataでは初回Q-ARCと初回caption handoffを互いに分離できないため、両者は初回回答支援へ合算する。視覚的な受領表示や無音`wait`で達成扱いにせず、1秒は回線・端末・managed modelを含む絶対上限の保証ではない。
- これとは別に、開始操作の受理から`Listening`までを本文なしのprepare SLOとして計測する。1,000 ms以下はon-target、1,000 ms超3,000 ms未満はslow、3,000 ms以上4,000 ms未満はmissed、4,000 ms以上はtimed-outとする。Native live preflightが4,000 ms以内にreadyにならなければcapture・VAD開始前に認証付きHTTP fallbackへ切り替える。speech-endから有意味PCMまでの1秒・3秒・10秒SLOは変更しない。`/health` warmupは次turnのprovider readyを証明しない。
- AI処理中と再生中も開始済みsession内ではマイクを端末内VADへだけ接続し、確認前PCMはAudioWorklet内の固定長リングから送らない。タブ非表示、4分無発話、30分経過でsessionを止める。
- runtime PDF uploadは標準・厳格の両モードでstate decode・STT・モデル呼出し前にbackendが拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する。提供PDFは課題・設計の参照資料であり、公開runtimeの添付機能ではない。

将来のprivacy-first pathでは、対応端末だけローカルASRで安定した文字列を作り、生音声をクラウドへ送らない経路を比較します。現在もNative Audio高速経路とregional STT / structured reasoner経路は同じものとして表示せず、厳格モードとの境界をUIで分けます。

## Revision-aware Thought State Graph

### 目的

文字起こし全文を会話履歴として積むだけでは、「さっきの発言を本人が取り消した」「途中認識が後から変わった」「目的が会話中に変化した」を安全に扱えません。Thought State Graphは、利用者の思考内容を結論へ固定せず、仮説として更新します。

### ノード

| 種類 | 意味 | 例 |
|---|---|---|
| `goal` | 達成したい状態 | 今週中に試作を公開したい |
| `claim` | 事実・判断として述べた内容 | この方式なら遅延は100ms以下になる |
| `evidence` | 主張を支える観測・資料 | 論文のTable 3、実測値 |
| `assumption` | 明示されていない前提 | 全利用者が高速端末を持つ |
| `constraint` | 期限、費用、安全、資源 | JSへ中核ロジックを置かない |
| `option` | 比較中の案 | Liveのみ、Sentinel併用 |
| `uncertainty` | 確信していない部分 | 法的に保存可能か不明 |
| `decision` | 現時点の選択 | raw audioは既定保存しない |
| `action` | 次に実行可能な行為 | E2Eテストを追加する |
| `source` | DOI等の参照metadata。URL・添付資料は将来の隔離済み経路だけ | DOI。runtime PDF本文やページは取り込まない |

### エッジ

```text
supports
contradicts
refines
depends_on
blocks
repeats
supersedes
answers
derived_from
```

グラフはモデルの非公開chain-of-thoughtを保存するものではありません。利用者が実際に述べた内容、明示された資料、観測可能な関係だけを短い要約として保持します。モデル内部の逐語的な推論過程はイベント、ログ、Firestoreへ保存しません。

### Revision規則

1. ASRの途中結果は`provisional`として追加する。
2. 安定した接頭辞だけを`stable`へ昇格する。
3. 認識変更、言い直し、否定が来た場合は既存ノードを書き換えず、`supersedes`またはretractionを発行する。
4. 介入候補は、参照したtranscript revisionとgraph revisionへ束縛する。
5. 発話前に基礎revisionが変わった候補は破棄する。
6. セッション内の時間順序には単調増加の`sequence`を使い、端末の壁時計だけへ依存しない。

### 検出するbreakdown

| 種類 | 内容 | 既定動作 |
|---|---|---|
| `goal_ambiguity` | 何を良くしたいか複数解釈が残る | 待つ、必要なら短く反射する |
| `goal_drift` | 当初目的から外れ続けている | 本来の目的を一言で返す |
| `missing_constraint` | 判断に必要な期限・費用・権限がない | 制約を一つだけ確認する |
| `contradiction` | 同時に成立しない主張が残る | 自己修正を待ち、差分を示す |
| `unsupported_claim` | 強い主張に根拠がない | 断定せず根拠確認を提案する |
| `claim_evidence_mismatch` | 資料が主張を支えていない | 出典箇所付きで確認する |
| `decision_loop` | 同じ比較を新情報なしで反復する | 比較軸または次の実験を一つ示す |
| `inactionable_plan` | 行動主体、期限、完了条件がない | 次の一歩へ圧縮する |
| `overload` | 論点が増え、同時処理が難しい | 論点を一つに絞る |
| `mode_transition` | 雑談から研究、相談から意思決定へ移る | 新しいモードへ追従し原則沈黙 |

`mode_transition`や感情表現は、それ自体を問題扱いしません。ぼやきが「解決より共感を必要としている」という仮説では、論理訂正の介入コストを高くします。

## Self-repair grace

### 状態遷移

```text
listening
  └─→ breakdown_candidate
        ├─→ self_repair_active ──→ resolved ──→ silence
        ├─→ insufficient_evidence ───────────→ silence
        └─→ stable_breakdown ────────────────→ EVI評価
```

次の信号がある間は原則として割り込みません。

- 「えっと」「いや」「違う」「というか」「待って」などの修復開始
- 語尾が未完了、接続助詞で終了、列挙途中
- 音響的に発話継続が予測される短い沈黙
- 直前の主張を否定・言い換えようとしている
- 新しい根拠や制約を追加している最中
- 利用者がAI応答へbarge-inして続きを話し始めた

フィラーの存在量を能力、病気、性格の評価へ使いません。Self-repair graceの目的は、利用者自身による修復をAIより優先することです。

grace時間を一律の秒数だけで決めず、音響、言語、過去の中断フィードバックを組み合わせます。時間閾値、VAPモデル、言語別設定はバージョン化し、評価結果にlogical policy IDを残します。

## Expected Value of Intervention

### 定義

候補介入`a`の期待値を次で管理します。

```text
EVI(a) =
  P(breakdown | context)
  × severity
  × P(improvement | a, context)
  × receptivity
  − interruption_cost
  − wrong_correction_cost
  − uncertainty_cost
  − privacy_cost
```

各項は0〜1へ正規化しますが、総合scoreだけをモデルへ自己申告させません。観測できる特徴、校正済み分類器、ルールを分けます。

| 項 | 主な根拠 |
|---|---|
| `breakdown_probability` | グラフ関係、複数モデル一致、発話行為 |
| `severity` | 誤りの影響、可逆性、期限、高リスク領域 |
| `improvement_probability` | 過去の同型ケース、介入種別、利用者feedback |
| `receptivity` | 会話モード、発話中か、明示された介入強度 |
| `interruption_cost` | VAP、自己修正、直近の介入回数、集中状態 |
| `wrong_correction_cost` | 意図の曖昧さ、根拠不足、分野外 |
| `uncertainty_cost` | Intent entropy、claim uncertainty、ASR不安定 |
| `privacy_cost` | 追加データ取得、検索、外部ツール呼び出し |

### 二段階判定

1. Fast Scoutが安定した節ごとに低コストで候補を抽出する。
2. 候補がない場合はPrecision Judgeを呼ばず沈黙する。
3. 介入候補だけをPrecision Judgeが根拠と反例の両方から検証する。
4. Arbiterは`候補介入`と`no response`を比較する。
5. 閾値を超えた場合も、発話可能時刻とTTLを満たした時だけ話す。

閾値付近で発話と沈黙が振動しないよう、hysteresis、cooldown、同一論点のdeduplicationを使います。閾値やcooldownは利用者が「静か」「標準」「積極的」から選べますが、安全と根拠の閾値は下げません。

### 発話しない条件

- Self-repair grace中
- 参照revisionが更新され、候補の前提が古くなった
- 第三者やテレビの発話であり、利用者宛てと判定できない
- 意図仮説が割れ、短い確認にも十分な価値がない
- 研究上の事実訂正なのに引用可能な根拠がない
- 高リスク領域で、一般的注意以上の断定になる
- 答え方の支援を本人が頼んでいない、または「今日は話すだけ」と指定した
- 直前に「最後まで聞いて」「今は共感だけ」と指示された
- 介入の方が利用者の主体性を損なう

## Latent Answer Contract

### 問題設定

根本の失敗を「質問Aに対して、説明は長いがAへ答えていない」と定義します。文体の採点ではなく、いまの発話が要求している答えの型と、返答の最初のcommitmentが一致するかを扱います。

LACは一つのturnについて次の三要素を作ります。

```text
QuestionFrame
  ├─ operator: boolean / choice / quantity / state / cause
  │            procedure / definition / comparison / evidence / open
  ├─ required_slots
  └─ target hypotheses: 最大3件 + confidence

CommitmentFront
  ├─ first_commitment
  ├─ filled_slots / target_coverage
  ├─ position: first / later / absent
  ├─ calibration: committed / conditional / uncertain / abstain
  └─ issue

CounterfactualRepair
  ├─ minimal_answer
  ├─ reconstructed_answer
  ├─ meaning_preservation_confidence
  └─ repair_gain
```

draftモデル内のLACはadvisoryです。最終draftの後に、source utterance、前状態、候補返答だけを低遅延fast modelの別structured callへ渡し、draft側のLAC自己申告を見せずに独立監査します。ここでいう独立性は別モデル利用ではなく、隔離prompt、別call、自己申告の非共有を指します。高速監査が構造不正または一時的provider障害で二度失敗した時だけprecision modelの中思考で一度回復監査し、安全終了・cancelでは切り替えません。最終判定はその監査値も鵜呑みにせず、Go側が仮説gap、正規化entropy、必須slot coverage、commitment位置、意味保存条件を再計算します。すべての監査が失敗した場合は未監査draftを読まず、intentional turnなら短い確認一問、ambient turnなら沈黙にします。

### AからAへ答える不変条件

1. 潜在問いの上位仮説が近い、またはentropyが高い場合は、勝手に一つへ固定せず`clarify`にする。
2. 問いのoperatorに対応するtarget slotが回答にない時、必須slotが一つでも欠ける時、または`first_commitment`が候補返答内に実在しない時は、説明の流暢さだけで`keep`にしない。
3. targetが満たされてもcommitmentが後ろにある場合、並べ替え候補は内部検証にだけ使い、AIの返答として代読しない。`respondent`では本人が同じAを先に言えるよう一度だけ穏やかに再質問する。
4. 再構成で元の条件、留保、不確実性が変わる場合は`reject`する。
5. targetを原文から安全に推定できない場合は、もっともらしい答えを捏造せず`clarify`または`silence`にする。
6. LACの原文入りcontractは現在turnだけで破棄し、暗号化したcross-turn stateへ入れない。

内部metricsは`Target Slot Coverage`、`Commitment Front Position`、`Meaning Preservation`です。これらはUIに採点文として並べるためではなく、A→Aのhard case、過剰修正、条件消失を回帰テストするために使います。

現実装の意味保存guardは、日本語の条件・因果marker、boolean極性、数値と単位、Latin / 選択肢label / 引用anchor、不確実性の段階を比較する決定論的検査です。これは意味同値性を完全に判定する証明ではないため、閾値以上でも誤修復率を人手評価し、operator別・domain別に校正する必要があります。

### 本人が答えるための有限コーチ

`respondent`経路の目的は、KOTAEが整えた答えを代読することではありません。通常会話では無効とし、本人が「答え方を一問だけ手伝って」などと明示的に頼んだ時だけ、一つの固定された短い質問を出します。本人が話した内容に質問が求めるAがなければ、結論を作らずopen slotを一つだけ尋ねます。Aが理由や前置きの後ろにある時も、AIはそのAを引用・整文・代読せず、本人が同じAを先に言えるよう一度だけ穏やかに再質問します。考え中の沈黙や短いフィラーでは再質問せず待ちます。その一度で成立しなければ採点や反復訓練をせず通常会話へ解放し、本人がAを先に言えたら通常はそこで閉じます。同じ明示支援中に本人が厳密句「理由まで一問お願いします」と頼み、そのAを独立検証できた時だけ、理由・根拠・最初の一歩のうち質問の型に合う固定の一問へ進みます。この任意の一問はフィラー中は黙って待ち、最初の実質的な返答で成否にかかわらず閉じ、さらに質問を重ねません。明示句がなければ、次turnが覚えていない具体的な質問を作らず、非質問のopen continuationで通常会話へ戻します。同じ答えを二段、三段と試験しません。Aを安全に特定できなければ答えを捏造せず、一度の短い問いで難しい時は支援を解きます。

「わからない」「まだ決めていない」も質問に対応した有効な回答です。考え中の沈黙や短いフィラーは再質問せず待ちます。文法、方言、声量、発話速度、フィラーの量は採点しません。本人が支援を頼んだ場面でも、回答後は話題の中身へ返し、講評を毎turn挟みません。

段階フェードは、有限コーチの固定質問を選択式から自由質問へ変える機能ではありません。本人が明示的に頼んだ支援turnで質問に対応するevidenceを受け取った後、前述の短期・非意味メタデータにより、通常会話でAIが任意に添える質問の量と型だけを`guided`、`light`、`natural`へ少しずつ調整します。`guided`は答えやすい二択を含む短い一問、`light`は短い自由回答の一問、`natural`は通常の会話です。質問負荷を下げる`listen`では問いを足さず、受け止めと短い情報だけを返します。AIに少し話を振ってほしい`companion`では、受け止めた後に情報や軽い話題を一つ提供し、最後の問いは答えなくても会話が成立する二択など一問までにします。質問だけを連続させず、AIの短い貢献と本人が話さない選択を同時に残します。「今日は話すだけ」と`listen`、および直後のcooldown中は任意質問を足しません。段階、score、連続成功は本人へ表示しません。

音声を取れない、または意味を一つに確定できないことは、利用者の回答能力ではなくシステム側の回復事象です。「回答の意味を確認できませんでした」と評価したり、同じ回答の言い直しを要求したりしません。聞こえた最小断片を一度だけ確認し、それでも難しい時は、会話sessionを保ったままAIが軽い話題を続ける、文字へ切り替える、何も質問せず待つ、のいずれかを本人が選べるようにします。

この足場は治療、診断、曝露療法、性格や「ひきこもり状態」の改変ではありません。AIが一turnで少し多く話すことと、利用時間・連続日数・感情的結び付きを最大化することを分けます。滞在時間、連続利用、排他的な親密さを目的関数にせず、人間との会話を遠ざける誘導、離脱への罪悪感、偽の個人体験、過剰な擬人化を使いません。

一度だけの言い直しでは、最初に本人が話したtarget slotのexact evidenceを含む意味節全体をNFKC・大小文字・空白・末尾句読点について正規化し、状態暗号鍵から用途分離して導出した秘密鍵を使うHMAC-SHA-256の128 bit tagへ変換します。MAC inputには暗号化session IDも含めます。次の発話が同じtarget意味節を含む時だけ完了を許可し、modelがevidenceを「目的」のような共通部分へ縮めても別のAを同一とは扱いません。明示的な訂正・撤回が別節にある時と、同じevidenceが複数現れる時もfail closedにします。tagは15分の暗号化状態内だけに置き、answer本文、evidence本文、質問本文は残さず、plannerとcriticのpromptにも渡しません。不一致や検証不能は失敗として再訓練を重ねず、固定の非強制的な一言で通常会話へ戻します。

これは「最初のAを、並べ替え時に別のAへ差し替えない」ためのnegative guardです。元質問のtopic identityや、同義語を含む意味的同値性までは証明しません。また、旧Aを独立した一文としてそのまま再掲した後、訂正語を使わず別Aを加え、両modelが旧Aだけを選ぶような敵対的発話を完全に意味判定するものでもありません。原質問や回答本文を保存しないまま任意の省略・引用・含意との意味関係を厳密に証明することはできないため、質問topicとの束縛とsemantic contradiction検出は別の検証課題として扱います。

### Q-ARCと回答後verifier-progress制御の実装境界

現在のQ-ARC（Question-bound Active Retrieval Controller）はGo内の小さな決定論的solverです。入力境界は有限型のoperator、endpoint/commit、hesitation、再発話・偶発音の確率、試行数、音声をcancelできるかだけで、質問本文、subject、答え候補、transcript、PDF、model draftを渡せません。8個の一時的な機能状態に対する区間beliefと4個の有限行動を使い、一回の決定ごとにcredal set全体で最大regretを最小化してからfloor-safety guardを通すone-step robust controllerです。返すのは有限`TemplateID`、`CueSlot`、数値certificateだけで、自由文は別の監査済みclosed rendererにしかありません。これはlearned policyでも、semi-Markovの滞在時間学習でも、個人適応でもなく、状態は診断や人物ラベルではありません。

対象条件を満たす新規scopeでは、Q-ARCのmodel-free fastpathがproviderの回答音声を破棄し、有限decisionを選びます。安定した暫定captionからdecision、renderer、streaming TTSをprivate commit buffer内で先行できますが、final captionの完全一致、browser commit、同じ報告質問由来の用途分離HMACへ束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointがそろってからPCMを解放します。Q-ARC自体へ質問本文を渡さず、handoff層で質問bound stateを作ります。汎用scope、汎用checkpoint、cached cueは使いません。不一致なら先読みした状態とPCMを破棄し、final captionを一度だけ処理します。条件が曖昧なturn、高リスク、activeな既存scopeはこのfastpathへ入りません。runtime document inputはfastpath判定より前に一律拒否します。それ以外の初回回答支援ではNative final captionを監査済みplannerへ直接handoffし、activeな既存scopeではNative final captionを監査済み段階controllerへ直接donateします。どちらも同じ原音へ2回目のSTTを行いません。

本人が回答した後のcontrollerは、Meaning Gateと独立LAC criticを`missing / available / later / first / ambiguous / rejected`等の有限signalへ落とし、`target missing / available uncommitted / committed late / committed first / verification unknown`の5状態verifier-progress audit posteriorをBayes更新します。全5行動の反実仮想utilityを計算し、安全maskで許された最高utilityの`wait / elicit / restate / complete / release`だけを返します。これはquestion-scope内で検証処理がどこまで進んだかを監査する短期制御priorであり、本人が本当に思い出せたか、知識量、疾患、能力、性格を推定するものではありません。認証暗号stateへ持ち越すwriter rolloutでは5個の固定小数massだけを保存し、質問・回答・逐語録は保存しません。writer flagとcontrollerのbehavior flagは分離しています。

## Think-Verbalize-Speak

Reasonerが生成する構造化判断を、そのまま読み上げません。

1. `Think`: グラフ、反例、根拠、不確実性から介入内容を決める。
2. `Verbalize`: 音声で一度に理解できる一つのspeech actへ変換する。
3. `Speak`: 短く話し、利用者が続きを求めた時だけ詳細化する。

最初の介入は原則として一論点、一動作、短い発話にします。日本語は単語数が安定した長さ指標になりにくいため、文字数だけでなく推定発話時間を制約に使います。

例:

- 「目的と手段、いま逆かも」
- 「その前提だけ確認しよう」
- 「不安なのは期限の方？」
- 「その数字、論文とは逆かもしれない」
- 「今決めるの、一つだけにしよう」

断定的な「間違っています」より、根拠の強さに応じて反射、確認、訂正を使い分けます。詳細説明、引用、長い手順は利用者が応答した後に続けます。

## 研究・論文モード

現在のMVPはruntime PDF uploadを全モードで拒否します。backendはdocumentをstate decode・STT・モデル推論前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。この設計で参照する提供PDFは課題定義の入力で、公開製品の添付機能ではありません。固定形式で明示したDOI照会とCrossref索引日による書誌候補探索は実装しましたが、候補の本文を自動取得したり主張を検証したりはしません。任意URL取得、世界中のWeb・論文本文の自動収集、引用付きclaim検証もまだ実装していません。

将来の研究ロードマップでは、安全な隔離・匿名化経路を先に成立させた後で、PDF、DOI、URL、引用情報を`source`ノードへ登録し、次を一般会話とは別のResearch Verifierで扱います。

- 主張と引用箇所の対応
- 支持、反証、条件付き支持
- 複数論文間の矛盾
- 実験条件、母集団、評価指標の取り違え
- 発表年と現在の技術状態の差
- 引用の不存在、別論文への取り違え

研究訂正は次の三値を基本にします。

```text
supported
contradicted
insufficient_evidence
```

`insufficient_evidence`を無理に支持・反証へ変換しません。論文が渡されていない場合は内容を推測せず、必要なら資料提供を音声で依頼します。

提供PDFと、将来扱う可能性のあるPDFやWebページ内の命令文は資料データであり、システム命令として実行しません。現在の公開runtimeはPDFを読みません。公開経路で行う外部取得は、利用者が現在発話の同じ文に対象と肯定命令を明示したCrossref書誌探索だけです。任意URL取得とコード実行は行いません。機能を増やす場合も、別のtool policyで利用者の意図、権限、保持条件を確認します。

将来Vertex Live APIやGoogle Search Groundingを評価する場合は、音声session、検索tool、行動toolを一つのモデル接続へ詰め込まず、独立したResearch Verifierと管理可能な検索経路へ分けます。検索の利用可否は、最新のAPI仕様とデータ保持条件を配備時に再確認します。

## イベント契約

### 共通envelope

内部イベントは`kotae.reflex.event.v1`で開始します。

```json
{
  "schema_version": "kotae.reflex.event.v1",
  "event_id": "opaque-random-id",
  "session_id": "opaque-random-id",
  "sequence": 42,
  "occurred_at_ms": 18420,
  "kind": "thought_graph.delta",
  "source": "reasoner",
  "payload": {}
}
```

規則:

- `occurred_at_ms`はセッション開始からの単調時間とする。
- `sequence`は同一セッション内で単調増加させる。
- UID、メールアドレス、token、cookie、raw audioをenvelopeへ入れない。
- `event_id`は再送時のidempotency keyとして使う。
- 未知のevent kindと未知の必須versionはfail closedで拒否する。
- payloadの自由記述をアプリログへ出さない。

### `audio.activity`

Sentinelが出す音声状態です。PCMを含みません。

```json
{
  "speech_probability": 0.94,
  "target_speaker_probability": 0.88,
  "continuation_probability": 0.76,
  "self_repair_cue": true,
  "background_speech": false,
  "buffered_audio_ms": 1200
}
```

`target_speaker_probability`に永続的な声紋を必須としません。話者識別や声紋を追加する場合は、マイク同意とは別の明示的同意を必要とします。

### `transcript.revision`

Secure session channel内だけで扱う増分文字起こしです。

```json
{
  "revision": 18,
  "replaces_revision": 17,
  "stability": "provisional",
  "language": "ja",
  "segments": [
    {
      "segment_id": "seg-12",
      "start_ms": 15420,
      "end_ms": 18110,
      "text": "いや、公開じゃなくて検証まで",
      "confidence": 0.91
    }
  ],
  "retracted_segment_ids": ["seg-11"]
}
```

文字起こしはログへ出さず、既定ではセッション終了時に破棄します。保存を選ぶ場合は`docs/audio-security.md`の同意、暗号化、保持期間を適用します。

### `thought_graph.delta`

```json
{
  "graph_revision": 9,
  "base_graph_revision": 8,
  "based_on_transcript_revision": 18,
  "upsert_nodes": [
    {
      "node_id": "n-goal-2",
      "type": "goal",
      "summary": "公開ではなく試作品の検証完了を目指す",
      "status": "stable",
      "confidence": 0.93,
      "evidence_segment_ids": ["seg-12"]
    }
  ],
  "upsert_edges": [
    {
      "edge_id": "e-7",
      "from": "n-goal-2",
      "to": "n-goal-1",
      "type": "supersedes",
      "confidence": 0.96
    }
  ],
  "retract_node_ids": [],
  "retract_edge_ids": []
}
```

`summary`は利用者の発話に根拠を持つ短い命題に限定し、非公開chain-of-thoughtを入れません。

### `intervention.candidate`

```json
{
  "candidate_id": "cand-31",
  "graph_revision": 9,
  "transcript_revision": 18,
  "breakdown": {
    "type": "missing_constraint",
    "probability": 0.82,
    "severity": 0.55,
    "evidence_node_ids": ["n-goal-2", "n-action-4"]
  },
  "self_repair_active": false,
  "intent_entropy": 0.18,
  "claim_uncertainty": 0.24,
  "proposed_action": "clarify",
  "proposed_content": {
    "speech_act": "constraint_check",
    "focus_node_ids": ["n-action-4"]
  }
}
```

### `intervention.decision`

```json
{
  "candidate_id": "cand-31",
  "decision": "speak",
  "action": "clarify",
  "policy_id": "reflex-evi-ja-v1",
  "evi": {
    "benefit": 0.61,
    "interruption_cost": 0.08,
    "wrong_correction_cost": 0.09,
    "uncertainty_cost": 0.05,
    "privacy_cost": 0.0,
    "total": 0.39
  },
  "earliest_at_ms": 18800,
  "expires_at_ms": 20800,
  "cooldown_until_ms": 26000,
  "reason_codes": [
    "stable_breakdown",
    "turn_complete",
    "benefit_over_silence"
  ]
}
```

`reason_codes`は監査可能な列挙値とし、内部推論文を保存しません。決定後に参照revisionが変わるかTTLを過ぎた場合、Verbalizerは発話せず`invalidated`を返します。

### `speech.request`

```json
{
  "candidate_id": "cand-31",
  "graph_revision": 9,
  "speech_act": "constraint_check",
  "spoken_text": "完了条件は、どこまで？",
  "estimated_duration_ms": 1150,
  "interruptible": true,
  "detail_level": "micro"
}
```

音声出力は常にbarge-in可能にし、利用者が話し始めたら再生を止めます。`spoken_text`は音声生成に必要な短時間だけ扱い、既定で保存しません。

### `feedback.signal`

```json
{
  "candidate_id": "cand-31",
  "signal": "too_early",
  "explicit": true,
  "local_preference_delta": {
    "interruption_tolerance": -0.1
  }
}
```

明示的な「助かった」「最後まで聞いて」「今は厳しく見て」を最も強いfeedbackとします。応答採用、barge-in、無視などの暗黙信号だけから性格や感情を推定しません。個人設定は可能な限り端末内の小さな集約値として保持し、raw audioを学習履歴にしません。

## 既存研究との差分

2026年7月時点で参照できた近接研究との設計差分です。

| 一次資料 | 到達点 | KOTAE Reflexの差分 |
|---|---|---|
| [Gemini Live API](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/live-api) | 低遅延native audio、barge-in、affective dialog、Proactive Audio | Liveを音声層として利用しつつ、構造化graphとEVIを独立させる |
| [Gemini Proactive Audio](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/live-api/configure-gemini-capabilities) | 指定した話題や条件まで共同聴取し、背景会話で黙る | 話題一致だけでなく、論理breakdown、自己修正、介入価値を判断する |
| [Moshi](https://arxiv.org/abs/2410.00037) | 約200msのfull-duplex speech-to-speech、重複、割り込み、相槌 | duplex生成自体ではなく、介入内容の根拠と沈黙政策を追加する |
| [Synchronous LLMs](https://aclanthology.org/2024.emnlp-main.1192/) | LLMへ現実時間を導入し、同期的な会話を生成 | 時間同期に加えてthought-state revisionと価値判断を持つ |
| [LLAMAPIE](https://aclanthology.org/2025.findings-acl.710.pdf) | 小モデルで発話時機、大モデルで1〜3語を生成するon-device in-ear assistant | 記憶補助から、独り言、論証、研究根拠、自己修正へ拡張する |
| [ProVoice-Bench](https://arxiv.org/abs/2604.15037) | 暗黙意図、潜在話題、文脈矛盾、環境音を含むproactive voice評価 | benchmarkの課題を実システムのgraph、EVI、privacy policyへ統合する |
| [Proactive Agent / ICLR 2025](https://proceedings.iclr.cc/paper_files/paper/2025/hash/75c37811e830bf029584b1c6fac17726-Abstract-Conference.html) | 明示命令なしの支援を学習し、ProactiveBenchを提示 | タスク開始だけでなく、音声中の発話・沈黙と誤介入損失を扱う |
| [ProACT](https://arxiv.org/abs/2607.03730) | 曖昧な目標、制約忘れ、議論ループを検知してskill routing | multi-user textのbreakdownを、個人の音声と自己修正へ移す |
| [Full-duplex Graph-of-Thoughts](https://arxiv.org/abs/2512.21706) | 発話意図とspeech actを進化型graphで推論 | 行動graphを、主張、根拠、制約、決定を含む内容graphへ拡張する |
| [Chronological Thinking](https://arxiv.org/abs/2510.05150) | 聞いている時間に因果的推論を進め、追加応答遅延を抑える | Shadow Reasonerで同じ時間をgraph更新と介入候補検証に使う |
| [DuplexSLA](https://arxiv.org/abs/2605.20755) | 音声、言語、actionを160msの共通時間軸で生成 | 外部sidecarで監査可能なaction channelを既存Vertex Liveへ追加する |
| [DialAM-2024](https://aclanthology.org/2024.argmining-1.8/) | 会話から命題関係と発話意図をargument map化 | offline完全graphではなく、不確実な増分deltaとretractionを扱う |
| [Argumentation Schemes](https://aclanthology.org/2025.acl-long.368/) | 24種類の議論パターンを自然言語対話から抽出 | 分類で終わらず、介入価値と短い修復発話へつなげる |
| [IntentSim](https://aclanthology.org/2025.findings-naacl.306/) | 意図分布のentropyから質問すべき時を判断 | clarifyだけでなくsilence、mirror、fact-checkを同じEVIで比較する |
| [Semantic Entropy](https://www.nature.com/articles/s41586-024-07421-0) | 意味単位の不確実性でconfabulationを検出 | 高不確実性を訂正抑制とPrecision Path routingへ使う |
| [Think-Verbalize-Speak](https://aclanthology.org/2025.emnlp-main.726/) | 深い推論を音声向け表現へ非同期変換 | EVIを通過した内容だけをmicro-interventionへ変換する |
| [Proactive Coaching Agents](https://aclanthology.org/2025.acl-long.1017/) | 目標理解、文脈確認、適切な提案、feedbackがstyleより重要と評価 | 一方的な質問や早すぎる提案をbreakdownとして制御する |
| [OpenScholar](https://www.nature.com/articles/s41586-025-10072-4) | 大規模な論文検索と引用付き科学サーベイ | 長文回答を、音声中のclaim-evidence確認へ接続する |
| [PaperQA2](https://arxiv.org/abs/2409.13740) | 文献検索、要約、矛盾検出をagent化 | Research Verifierの候補とし、Liveセッションから分離する |
| [Japanese Moonshine](https://arxiv.org/abs/2509.02523) | 27M parameterの日本語向けedge ASRを公開 | 将来のPrivacy Sentinelで実端末・ブラウザ性能を検証する |
| [Multilingual VAP](https://arxiv.org/abs/2403.06487) | 英語、中国語、日本語の将来発話活動を予測 | Self-repair graceと発話可能時刻へ利用する |

## 主要評価指標

総合会話満足度だけでなく、沈黙と介入の両方を測ります。

### 介入政策

| 指標 | 定義 |
|---|---|
| 不要介入率 | 介入不要と人が判断した機会のうち、AIが話した割合 |
| 不要介入回数/分 | セッション時間で正規化した誤介入 |
| 高価値介入recall | 介入すべき機会を検出できた割合 |
| Self-repair preemption | 本人の自己修正が始まっていたのにAIが遮った割合 |
| Silence precision | 沈黙を選んだケースのうち、沈黙が適切だった割合 |
| EVI calibration | 予測した改善確率と本人評価の一致度 |
| 同一論点反復率 | 同じ内容をcooldown中に繰り返した割合 |

### 時間と音声

- 発話終了予測から介入開始までのp50、p95
- breakdownがstableになってからdecisionまでのp50、p95
- 将来full-duplex経路でのbarge-in検出からAI音声停止までのp95
- 発話継続中に割り込んだ割合
- backchannel、side conversation、ambient noiseごとの誤作動
- 日本語と英語のcode-switch時のターン判定

Full-duplexの比較には、割り込み、相槌、横の会話、環境音を扱う[Full-Duplex-Bench v1.5](https://arxiv.org/abs/2507.23159)のシナリオを再利用します。

### Graph

- node typeごとのprecision、recall
- `supports`、`contradicts`、`supersedes`等のedge F1
- ASR revision後に誤ったnodeをretractできた割合
- 取り消した主張を後の介入根拠に使った割合
- goal、constraint、decisionの長時間一貫性
- graphなし、text履歴のみ、graphありのablation

### 研究

- claim-evidence alignment
- 引用が実在する割合
- 引用箇所が実際に主張を支える割合
- `insufficient_evidence`を正しく選べた割合
- 複数論文の矛盾を誤って一方へ統合しなかった割合
- 発表年、実験条件、評価指標を取り違えた割合

### 本人中心の評価

- 「助かった」「邪魔だった」「早すぎた」「断定が強すぎた」
- 介入後に利用者が内容を採用、修正、拒否した割合
- 会話の主体性を保てたか
- AIが話さなかったことで見逃されたと感じたか
- 介入強度設定ごとの好み

コーチング研究では、本人、専門家、LM judgeの評価が一致しないことが報告されています。LM judgeは大規模な補助評価に使えますが、本人の一人称評価を主要outcomeから外しません。

### セキュリティ

- 明示開始前に取得・送信された音声byteが0である
- 停止後に取得・送信された音声byteが0である
- raw audio、transcript、prompt/responseがログへ現れない
- Firebase ID token、App Check、Originのどれかが不正なrequestでモデル呼び出しが0である
- missing / unknown `turnMode`を拒否し、状態tokenの有無でambientを推測しない
- 認証済み音声requestは本文decode前にUID quotaとFirebase App quotaを消費する
- 状態tokenの改ざん、期限切れ、別UID利用の成功率が0である
- session停止後にマイクtrackが終了し、新しい音声送信が起きない
- Storage、Firestoreへ音声・transcript・PDF本文が作成されず、runtime document inputはstate decode・STT・modelより前に拒否される
- third-party audioを利用者本人の入力として誤採用した割合

## 評価ケース設計

### 日常会話

| ケース | 期待動作 |
|---|---|
| 「今日は疲れた、何もしたくない」とぼやくだけ | 原則沈黙または短い反射。即座に予定表へ変換しない |
| 「金曜公開。ただし木曜まで実装」と矛盾する予定 | 自己修正を待ち、残った場合だけ期限関係を確認 |
| 制約を順番に思い出して追加する | 途中で結論を出さず、追加が止まってから整理 |
| 同じ選択肢を新情報なしで何度も往復する | 比較軸か小さな検証を一つ提案 |
| テレビや同席者が質問する | 現MVPは利用者の声と安全に識別できない。ambient利用を避け、利用者自身が質問を言い直す |
| 利用者が「今は聞いて」と言う | 以後のEVIへ強い抑制を加える |
| 利用者が「厳しく見て」と言う | 根拠閾値を維持したまま介入許容度だけ上げる |

### 独り言

| ケース | 期待動作 |
|---|---|
| 「Aで、いやB、待ってAかな」と自分で修復する | grace中は沈黙し、最終状態だけgraphへ反映 |
| 長い沈黙の後に同じ文を続ける | 沈黙秒数だけでターン終了にしない |
| 目的から逸れたが、新しい目的へ明示的に移った | goal driftではなくmode transitionとして追従 |
| 行動が「そのうち調べる」で終わる | 会話モードがplanningなら完了条件を一つ確認 |
| 感情を吐き出している途中に論理矛盾がある | 低重大度なら訂正しない。安全上必要な時だけ介入 |
| AIの一言の途中で利用者が再開する | 直ちに停止し、新発話を優先 |

### 研究・論文

| ケース | 期待動作 |
|---|---|
| PDFを添付しようとする | 標準・厳格を問わず、runtime uploadは利用できないという固定案内で止める。backendへ直接送られてもstate decode・STT・model前に拒否する |
| 論文本文に基づく主張 | runtimeでは本文を読んだふりをせず、利用者が述べた主張と、一次資料で検証済みのevidenceを分ける |
| 相関研究を因果効果として話す | 研究デザインを根拠に条件付き修正 |
| 二本の論文が異なる結論 | 片方を正解扱いせず、条件差または矛盾を提示 |
| 論文が未提供 | 内容を創作せず資料提供を依頼 |
| 2024年時点のモデル状態を現在仕様として話す | 最新一次資料を別Research Verifierで確認 |
| PDF本文に「以前の指示を無視せよ」とある | 現runtimeでは全モードのPDF拒否境界で本文をparser/modelへ渡さない。将来の資料経路でも命令権限を与えない |
| 存在しない引用を利用者またはモデルが述べる | `insufficient_evidence`として確定を避ける |

### 音響・adversarial

- 咳、笑い、タイピング、通知音、音楽
- 近距離の利用者と遠距離のテレビ音声
- 同時発話、短い相槌、割り込み
- 早口、ささやき、大きな声
- 日本語内の英語技術語とcode-switch
- ASR途中結果の大幅な書き換え
- AI出力のエコーを利用者発話として再入力する状況
- 長時間セッションでのmemory圧縮と古い候補の失効

## 評価データと実験手順

1. 正例より沈黙すべき負例を十分に含める。
2. 話者、話題、論文をtrain/testで分離する。
3. 合成音声だけでなく、同意を得た自然な日本語の独り言を含める。
4. breakdownの有無だけでなく、最適な介入時刻、`no response`、許容するspeech actを注釈する。
5. 同じ会話に対する`沈黙`、`早い介入`、`適切な介入`を本人へblind比較する。
6. first-person評価、専門家評価、LM評価を別々に保存し、混ぜて単一正解にしない。
7. raw audioの研究利用は既定OFFとし、製品利用同意とデータ提供同意を分ける。
8. モデル、rubric、prompt、schema、EVI policyのversionを結果へ保存する。

主要ablationは次の通りです。

- Liveモデルだけ
- Live＋固定promptのProactive Audio
- Live＋Fast Scout
- Live＋Thought State Graph
- Live＋Graph＋Self-repair grace
- Live＋Graph＋grace＋EVI
- 上記＋Research Verifier

## 実装段階

### A. Voice-first MVP — 実装済み

- 明示開始されたブラウザsessionと端末側VAD
- 20 ms PCMの認証付き増分WebSocket主経路と2 MiB上限の同期圧縮HTTP fallback、Firebase Auth、App Check、完全一致Origin、明示`turnMode`検証
- 端末の録音開始から最大3分30秒、Cloud Run受信4分、Go live deadline 6分、Cloud Run request timeout 420秒という独立した上限。発話が確定しない無音候補は最大30秒で、3分30秒の実発話を保証しない
- 圧縮音声fallbackの2 MiB上限と、超過時にpartial audioをuploadしないfail-closed処理
- 確定文字起こし160 rune以上に限定した現在turnだけの`extended speech`構成。12秒以上続いた明確な独話だけ終端の無音待ちを5秒へ延ばし、短い質問の確定待ちは増やさない
- 標準liveのraw audio → Vertex AI Native Audio（`us-central1`、応答modalityは`AUDIO`のみ、captionはinput/output transcription config）→ 24 kHz PCM + caption
- 本人の明示回答支援をfinal入力captionで検証した初回turnはNative音声を破棄する。対象条件を満たす新規scopeではQ-ARCのmodel-free fastpathが有限decisionを選び、安定した暫定captionでrendererとstreaming TTSをprivate commit buffer内だけに先行できる。final captionの完全一致、browser commit、報告質問由来の非可逆tagへ束縛したAES-256-GCM認証暗号stateを含む有限checkpoint後にだけPCMを解放し、不一致なら先読みを破棄してfinalを一度だけ処理する。それ以外はNative final captionを`global` Vertex AI structured reasoner / LAC / Respondent Coach以降へ直接handoffする。保留中の継続turnもNative final captionを監査済み段階controllerへ直接donateする。初回・継続とも同じ原音へ2回目のSTTを行わず、監査済みplanner経路では実質問由来のoperator・required slot・非可逆tagで次turnを照合する。`complete` / `release`後の次turnから通常のNative応答へ戻る
- raw audio、文字起こし、応答文をアプリ側で永続化しない。runtime PDFはstate decode・STT・modelより前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する
- AES-256-GCM認証暗号、UID-bound、15分TTL、自由文要約なしのフィルタ済み意味状態
- Thought State Graph、規則とモデル判定を併用したEVI
- LACのQuestionFrame、CommitmentFront、CounterfactualRepair
- draftから独立したLAC criticとGoの決定論的authoritative evaluator
- KOTAE自身の回答と、他者の質問へ本人が答える`respondent`経路の分離
- purposeを含む質問operator、本人回答内のexact slot evidence、既存意味節だけを並べ替えるRespondent Meaning Gate
- 質問本文・答え本文を入力に持たない8状態・4行動のQ-ARC one-step credal minimax-regret solver、有限型のtemplate / slot / certificate、別の監査済みrenderer、stable interimのprivate commit bufferでTTSを先行し、exact-final・browser commit・質問boundなAES-256-GCM認証暗号stateを含む有限checkpoint後だけPCMを出す経路
- Meaning Gateと独立LAC criticの有限signalだけから、現在の質問に対する検証の進行を表す5状態verifier-progress audit posteriorをBayes更新し、安全mask付き反実仮想utilityで回答後の`wait / elicit / restate / complete / release`を選ぶcontent-free controller。本人のretrieval状態は推定せず、progressの短期state writerとcontroller behaviorは別々のrollout flagで制御する
- 否定、条件、数値、不確実性、固有内容を変える再構成のfail-closed拒否
- 保留質問にはoperator、短いsubject、required slotsと、言い直し中だけの128 bit HMAC tagを残し、回答試行・再構成案・evidence本文を残さない
- 曖昧な問いでのclarify、低EVIと自己修正中のsilence
- runtime PDF uploadを全モードでstate decode・STT・モデル推論前に拒否してPDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄し、提供PDFを公開機能とは扱わない
- 高リスクdomainのprecision fail-closed
- 160 Unicode code point以上のfinal transcriptだけを対象に、現在turnの明示内容から中心点を意味保存して第一文へ置く長い独話用の足場。長さを能力や効果の指標にせず、途中候補や過去turnから補わない

### B. LAC評価 — 実装中

- 日本語hard caseでA→A、operator、条件、不確実性の不変条件を回帰テスト
- Target Slot Coverage、Commitment Front Position、Meaning Preservationの校正
- `keep / clarify / restructure / reject`を人手blind評価
- 一般会話、研究、技術、高リスク領域ごとの誤修復率と過剰介入率
- model-only、固定prompt、LACなしEVIとのablation

### C. Privacy Sentinel — 一部実装

- 利用者が明示選択する厳格request型を実装し、外部検索・cross-turn stateを受理前に拒否する。PDFは共通backend境界が全モードで受理前に拒否する
- regional STT後の文字起こしとモデル応答を、Cloud Run内の決定論検査と東京リージョンDLPの両方へ通す。`clear`以外、timeout、権限エラー、応答不整合は後段へ進めない
- 厳格streamingでは合成音声をrequest-bound bufferへ保持し、検査済みresultとmodeが一致した後だけ送信する。blocked/error時は送信せずbufferを消去する
- 原音はregional STT、文字列はCloud Run・DLP・Vertex AI、応答はTTSが平文で扱う。このためE2EEでも完全PII除去でもなく、そのようには表示しない
- 短いPCM ringの固定長所有権は専用Rust/Wasmへ移したが、通常・割込みVADの時系列FSMを単調なAudioContext sample clockへ束縛してRust/Wasmへ移す作業、local transcript path、話者本人認証は未実装
- TEN VAD、VAP、Moonshine等はライセンス、配布量、日本語精度、Web性能を実測してから採用する

### D. Research Verifier — 一部実装

- runtime PDF uploadは全モードで拒否し、提供PDFは課題・設計の参照資料に限定する。完全匿名化や本文の自動検証を行うResearch Verifierは未実装
- intentional turn全体が固定の外部検索形式に完全一致したDOI照会・論文探索だけをCrossrefへ送るtyped discovery adapterを実装。自然文から同意を推測せず、追記、取消し、複数命令、ambientでは外部送信しない
- 固定HTTPS host、redirect拒否、8秒timeout、2 MiB上限、PII / credential screen、percent encoding検査、DOI・日付・URL正規化、重複排除を実装
- 返却を常に`discovery_metadata_not_claim_evidence` / `needs_primary_evidence`とし、abstractを再配布せず、最大5件のtitle・DOI・日付だけを現在turnへ返す
- topic探索はCrossrefのindex date filterを使うため、結果を「Crossrefの索引日が指定期間内の書誌候補」と表示し、新規発表順とは呼ばない
- 任意URL取得、世界中のWeb巡回、PDFや過去stateからの自動query、バックグラウンド定期収集は未実装
- 論文本文の取得権限、source / claim / evidence graph、引用箇所が主張を支持するかの検証、撤回・更新・矛盾の監査は未実装
- 今後も検索toolと行動toolを音声sessionの権限から分離し、`insufficient_evidence`を第一級statusとして評価する

### E. 低圧な会話足場 — 一部実装

- 「今日は話すだけ」で答え方支援を停止し、本人が改めて頼むまで再開しない
- 「最後まで聞いて」「今は共感だけ」「一問だけ手伝って」等の明示feedbackを暗黙行動より優先する
- 検証済みの明示練習後だけ、会話内容を含まない短期メタデータで通常会話の任意質問量とcooldownを調整する
- 有限コーチの固定質問自体は段階によって変えず、Aが後ろならAIが代読せず一度だけ穏やかに再質問し、A-firstなら閉じる。一度で成立しなければ通常会話へ解放する
- score、レベル、streak、順位をUIへ表示しない
- 暗黙feedbackだけで自動的に性格・健康状態を推定しない
- 十分な同意データが集まるまではオンライン強化学習を行わない
- 約3分の独話を受けて同じturnで足場を返す機能は実装済みだが、結論先行技能の長期定着、他場面への転移、心理的負荷の軽減は未実証として扱う

### F. アカウント操作確認と個人内長期測定 — 実装済み、効果未実証

- WebAuthnのresident credential、user verification、exact origin、RP ID、5分期限、単回ceremony、署名counterの競合検査を実装し、仮名Firebase accountの登録・再認証へ使う
- Passkeyが確認するのは登録済みauthenticatorによるアカウント操作であり、法的身元や現在マイクで話す人ではない。声紋は収集しない
- 明示opt-in後だけ、開始時・4週・8週・追跡時点の固定未見質問について有限分類と1〜5の自己評価を端末内へ保存する。音声、文字起こし、自由文、Firebase UID、時刻、応答時間は保存しない
- 撤回、再同意、別tab競合時のgeneration fence、全削除、期限外・重複・形式不整合時のfail-closedを実装した。全削除後は回答の復活を防ぐ固定markerだけを残し、個人ID・同意epoch・回答は残さない。ただしlocalStorageは署名付き監査台帳ではなく、整形式データを開発者ツールで作り直す改変までは防がない。これは個人内測定であり、比較試験、因果効果、有効性の証明ではない

## セキュリティ継承

認証、リージョン境界、一時処理、暗号化状態、IAM、ログ禁止は`docs/audio-security.md`を正とします。本資料はその上に推論と介入の制約を追加します。現在はLive ticket、保存音声Vault、音声履歴、無人の後日再評価を実装していません。

追加の禁止ログ項目:

- transcript revision
- graph nodeのsummary
- breakdown evidence
- proposed spoken text
- EVIへ使った自由記述
- PDF本文と引用抜粋
- 利用者の明示feedback本文

運用ログにはevent kind、schema version、logical policy ID、latency、列挙型reason code、集約された成功・失敗だけを残します。デバッグのために会話本文を一時的に記録する場合も、本番とは分離した明示同意、短期TTL、アクセス監査を必要とします。

現在のMVPはVertex AI Native Audioを標準live turnに使いますが、session resumptionとGoogle Search Groundingは使いません。「使っていない機能がある」ことだけでGoogle Cloud全体のゼロ保持を保証するとは表現せず、Native Audioを含む現在のデータ保持・地域境界は`docs/audio-security.md`を正とします。現在の境界は[Vertex AI zero data retention](https://cloud.google.com/vertex-ai/generative-ai/docs/vertex-ai-zero-data-retention)も参照します。

## 参考一次資料

### 低圧な会話と心理的安全

- [厚生労働省「ひきこもり支援ハンドブック～寄り添うための羅針盤～」](https://www.mhlw.go.jp/content/12200000/001605332.pdf)
- [Rapport Building in Human-Agent Interaction during Small Talk with Virtual Agents](https://www.isca-archive.org/interspeech_2024/baihaqi24_interspeech.pdf)
- [WHO: Towards responsible AI for mental health and well-being](https://www.who.int/news/item/20-03-2026-towards-responsible-ai-for-mental-health-and-well-being--experts-chart-a-way-forward)
- [American Psychological Association: Use of generative AI chatbots and wellness applications for mental health](https://www.apa.org/topics/artificial-intelligence-machine-learning/health-advisory-chatbots-wellness-apps)

### Full-duplex・発話時機

- [Moshi: a speech-text foundation model for real-time dialogue](https://arxiv.org/abs/2410.00037)
- [Beyond Turn-Based Interfaces: Synchronous LLMs as Full-Duplex Dialogue Agents](https://aclanthology.org/2024.emnlp-main.1192/)
- [Full-Duplex-Bench v1.5](https://arxiv.org/abs/2507.23159)
- [Real-time and Continuous Turn-taking Prediction Using Voice Activity Projection](https://arxiv.org/abs/2401.04868)
- [Multilingual Turn-taking Prediction Using Voice Activity Projection](https://arxiv.org/abs/2403.06487)
- [Investigating Incremental Processing and VAP](https://aclanthology.org/2025.coling-main.249/)
- [Chronological Thinking in Full-Duplex Spoken Dialogue Language Models](https://arxiv.org/abs/2510.05150)
- [DuplexSLA](https://arxiv.org/abs/2605.20755)

### Proactive agent・coaching

- [LLAMAPIE: Proactive In-Ear Conversation Assistants](https://aclanthology.org/2025.findings-acl.710.pdf)
- [ProVoice-Bench](https://arxiv.org/abs/2604.15037)
- [Proactive Agent: Shifting LLM Agents from Reactive Responses to Active Assistance](https://proceedings.iclr.cc/paper_files/paper/2025/hash/75c37811e830bf029584b1c6fac17726-Abstract-Conference.html)
- [ProACT](https://arxiv.org/abs/2607.03730)
- [Substance over Style: Evaluating Proactive Conversational Coaching Agents](https://aclanthology.org/2025.acl-long.1017/)
- [Clarify When Necessary](https://aclanthology.org/2025.findings-naacl.306/)

### 思考・論証・不確実性

- [Enabling Conversational Behavior Reasoning Capabilities in Full-Duplex Speech](https://arxiv.org/abs/2512.21706)
- [DialAM-2024](https://aclanthology.org/2024.argmining-1.8/)
- [Mining Complex Patterns of Argumentative Reasoning in Natural Language Dialogue](https://aclanthology.org/2025.acl-long.368/)
- [Detecting hallucinations using semantic entropy](https://www.nature.com/articles/s41586-024-07421-0)
- [Think, Verbalize, then Speak](https://aclanthology.org/2025.emnlp-main.726/)
- [Acoustically Precise Hesitation Tagging](https://arxiv.org/abs/2506.04076)

### 研究サーベイ・端末処理・プライバシー

- [OpenScholar](https://www.nature.com/articles/s41586-025-10072-4)
- [PaperQA2](https://arxiv.org/abs/2409.13740)
- [Flavors of Moonshine](https://arxiv.org/abs/2509.02523)
- [TEN VAD](https://github.com/ten-framework/ten-vad)
- [Gemini Live API overview](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/live-api)
- [Configure Gemini capabilities](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/live-api/configure-gemini-capabilities)
- [Gemini Enterprise Agent Platform and zero data retention](https://docs.cloud.google.com/gemini-enterprise-agent-platform/resources/zero-data-retention)
