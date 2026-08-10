# コタエーアイ(AI)

コタエーAIは、本人の答えを代作したり、採点して一律に矯正したりするアプリではありません。相手から聞かれた質問と、まとまらないまま話した本人の回答を分けて捉え、「A」を本人自身の言葉で先に返せるよう、普通の音声会話の中で必要な時だけ短く支えます。質問拘束・入力由来・A先頭を独立に確認できた完了turnでは、AIの相づちや講評さえ生成せず、本文を含まない回答所有権レシートだけを出して発話権を本人へ返します。通常の質問、日常のぼやき、考え途中の独り言、研究や論文の話にも応じ、本人の言い直しで解決しそうな時は割り込みません。会話を課題や訓練にせず、短い一往復だけでも終えられます。この会話支援が回答能力を長期に向上させることはまだ実証していません。

公開版: [https://kotae-ai.web.app](https://kotae-ai.web.app)

## 作品の核

- **KOTAE Reflex**: 発話から目標、主張、根拠、制約を短いThought State Graphとして更新し、自己修正を待つ時間とExpected Value of Intervention（EVI）を使って、話すか沈黙するかを決める
- **Latent Answer Contract（LAC）**: 潜在的な問いを最大3候補まで仮説化し、問いが要求する型と回答冒頭のコミットメントを照合する。問いが曖昧なら決めつけず、答えの核が後ろへ埋もれた時だけ、条件と不確実性を変えない再構成を許可する
- **Respondent Coach + Meaning Gate**: 「AIへの質問」と「相手から本人へ向けられた質問」を区別する。「代わりに答えて」「回答を作って」「この答えを読み上げて」という本人の依頼も、代理回答を生成せずモデル呼び出し前に本人のAスロットへ変換する。逆に「代わりに答えないで」「回答を作らないで」と明示したturnは、Native providerの音声と字幕を公開せず、モデルを呼ばない固定応答へ渡す。この拒否は新しいAスロットを開かず、すでに本人が始めた質問拘束済みscopeも勝手に破棄しない。後者では本人の回答内に完全一致するslot evidenceを束縛して判定するが、AIの再構成案を本人の答えとして読み上げない。Aが後ろなら固定文で一度だけやさしく聞き直し、次は普通の会話へ戻る。Aを先に言えたら通常はそこで閉じ、本人が同じ支援中に厳密句「理由まで一問お願いします」と明示した時だけ、理由・根拠・最初の一歩のうち質問の型に合う一問を足す。この任意の一問は二段目の合格試験にせず、最初の実質的な返答で閉じる。考え中の「えっと」「うーん」は失敗回数にせず、`wait`として音声を返さず話す番を守る
- **Q-ARC + Verifier Progress Controller**: 新しい回答支援scopeでは、質問本文・回答候補・transcript・model draftを入力型に持たない質問拘束済みQ-ARCが、有限template IDとslotだけを選ぶ。別の監査済みclosed rendererだけが「最初の一言だけで大丈夫です」のようなopen-slot cueへ変換する。安定した暫定captionではdecisionとstreaming TTSをprivate commit buffer内で先行できるが、final captionとの完全一致、browser commit、質問へ用途分離HMACで束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointがそろうまでPCMを解放しない。不一致なら先読みを破棄し、final captionを一度だけ処理する。本人が答えた後は、Meaning Gateと独立LAC criticの有限signalから、現在の質問に対する検証の進行だけを表す5状態verifier-progress audit posteriorをBayes更新し、`wait / elicit / restate / complete / release`の反実仮想utilityを比較する。これは本人のretrieval状態、診断、知識の有無、正答、長期的な上達や内面を推定する仕組みではない。audit posteriorの短期stateへの保存と、このcontrollerの使用は別々のrollout flagで制御する
- **Question-Bound Answer Ownership Proof（QBA Proof）**: 入力内で報告された質問span、質問主題、確定入力の全required-slot evidenceを、用途分離した非可逆HMACへ別々に束縛する。確定音声入力の現在turnにあるexact spanだけを対象に、決定論的Gateと独立LAC criticがともにcoverage=1かつA-firstと判断した時だけ、本文を含まない固定enumを画面へ返す。AI draft、再構成案、暫定認識、A-later、別質問、無関係な次turn、監査不能では発行しない。第三者が現実に質問した事実、正解、能力、上達、話者の身元、録音の真正性を証明するものではない
- **Research discovery**: 本人のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した時だけ、固定語の間にあるtopicまたはbare DOIをCrossrefへ最小送信する。返すのはCrossrefの索引日が指定期間内の書誌候補であり、論文の発表日順でも、本文や主張を検証済みとした結果でもない
- **音声向け出力**: 内部の分析を画面へ大量表示せず、必要な一つの介入だけを短い日本語音声へ変換する
- **端末内の長期測定**: 明示的に参加した人だけを対象に、固定された未見質問への自己評価をbaseline・4週・8週・追跡時点で端末内へ記録し、2時点以上では有限項目の生値だけを表示する。会話本文や音声は記録せず、撤回、競合時の停止、削除を実装しているが、比較試験でも改変耐性のある研究台帳でもなく、長期効果は未実証

KOTAE ReflexとLACは、このプロジェクトで設計・実装している実験的な仕組みです。近接研究を踏まえていますが、「世界初」や有効性が確立済みとは表現しません。新規性の評価は、既存APIの組み合わせではなく、潜在問いの同定、答えの先頭契約、意味保存を伴う修復が実測で機能するかによって行います。

## 現在の音声経路

```text
Rust / Dioxus / Wasm UI
  └─ ブラウザ境界: MediaRecorder / Web Audio、Rust/Wasm PCM ring、Firebase SDK
       └─ Firebase Hosting /api rewrite または固定Cloud Run URLへ認証付きTLS
            └─ WebSocket、HTTPS stream、またはPOST /api/v1/voice/turns
                 └─ Cloud Run / Go
                  ├─ 標準live: raw PCM ──→ Vertex AI Native Audio（us-central1）──→ PCM + caption
                  │    ├─ 対象となる新規回答支援: provider出力破棄 → ローカルQ-ARC + private streaming TTS
                  │    ├─ それ以外の初回回答支援: final input captionを監査済み段階plannerへ直接引継ぎ（2回目のSTTなし）
                  │    └─ 継続回答支援: Native final captionを同じ段階controllerへdonate（東京STTの二重通過なし）
                  ├─ 厳格 / 接続fallback
                  ├─ Cloud Speech-to-Text V2（asia-northeast1）
                  ├─ 厳格モード: Cloud Run内の決定論的検査 + Sensitive Data Protection（asia-northeast1）
                  ├─ 文字列 ──→ Vertex AI（global）: KOTAE Reflex + LAC
                  ├─ Crossref（明示したDOI / 新着論文検索だけ）
                  └─ Cloud Text-to-Speech（asia-northeast1）
                  └─ runtime PDF upload: state decode・STT・モデル推論前に一律拒否
```

マイクは利用者が明示的に開始したセッション中だけ使います。端末側VADが一つの発話を区切り、認証済みのWebSocketを優先し、使えない時だけ認証済みHTTPSへ退避します。低遅延streamとWebSocketは固定したCloud Run URLへ直接CORS/TLSで接続し、同じ仮名アカウントのlive接続はFirestoreの短命leaseで同時に1本へ制限します。長い独話はクライアント最大3分30秒、サーバー最大4分で安全に区切り、Cloud Runの420秒timeoutより内側で終了します。最後の声から700 ms無音になった時点で、内容を理解したとは主張しない「ここまで届いています」を端末上に表示し、発話再開時は即座に消します。この視覚的な受領表示はダミーの固定音声を再生せず、意味応答の開始とも数えません。標準liveの通常会話はNative Audioで段階的なSTT・推論・TTS待ちを避け、発話終了から最初の実質音声frameまで1,000 ms以内を運用SLOとして計測します。1秒は回線、端末、managed modelを含む絶対上限の保証ではありません。初回回答支援も2回目のSTTを行わず、対象条件を満たす時は質問拘束された有限型Q-ARC、それ以外はNative final captionの直接handoffを使います。実測は通常会話と分けますが、現行のroute metadataではQ-ARCとcaption handoffを互いに分離できないため、両者は「初回回答支援」へ合算します。

標準Native turnでは、WebSocketの`ready`を認証・quota・UID leaseの完了だけでは送りません。そのturnがproviderを使う場合は、Vertex AIから`SetupComplete`を受け取り、`StartActivity`の送信にも成功した後にだけ`ready`を送ります。ブラウザは開始操作で取得したマイクtrackを準備中はmuteし、`ready`より前にクラウドPCM captureを接続せず、VADを開始せず、画面を`Listening`へ変えません。Native live preflightが4,000 ms以内に`ready`へ達しなければ、クラウドPCM captureとVADを始める前に認証付きHTTP fallbackへ切り替えます。開始操作の受理から`Listening`までを本文なしの別SLOとして、1,000 ms以下を目標内、1,000 ms超3,000 ms未満をslow、3,000 ms以上4,000 ms未満をmissed、4,000 ms以上をtimed-outとして表示します。これは発話終了から最初の有意味PCMまでの1秒目標・3秒miss・10秒停止SLOとは別で、後者の境界は変えません。`/health`へのwarmupはprocessの応答だけを確かめるもので、次のturnのprovider readyを証明しません。providerを使う各browser turnは新しい接続を所有し、turnをまたぐprovider sessionのpooling・resumption・再利用はまだ行いません。

AI応答中の割り込み候補は、すでに利用者が開始したsessionの端末内VAD、bounded MediaRecorder、対応端末では固定長PCM ringで確認します。AECを確認でき、stateful drainが不要なpre-final Native liveとPCM handoffがそろった時だけ、新しいNative turnを裏で準備します。この候補PCMはstrong ready前に新providerへ送信・adoptせず、発話終了後はNative readyを最大450 ms待って、間に合わなければHTTP fallbackへ切り替えてから新turnを`Listening`へ進めます。それ以外の割り込みは新providerを準備せず、既存のbounded MediaRecorderを使うHTTP経路として直ちに`Listening`へ進みます。短いぼやきは既存のhard interruption gateを満たさないため、応答を中断しません。画面の「音声入力準備済み」は入力受入境界だけを表し、監査済みno-provider fallbackでproviderを開いた証明には使いません。

1.6秒未満の短い明瞭な標準Native発話は、providerのendpoint通知と端末VADが一致した場合に280 ms、通知がない場合も400 msの局所無音でturnを確定します。1.6秒以上の発話、検証済みの継続発話、静かな発話、長い独話、回答支援中の継続発話にはこの短縮を適用せず、それぞれの長い待機窓を維持します。Native Audio、HTTP fallback、厳格経路のいずれも、最初の有意味PCMを端末出力時刻へ配置できた時だけ、発話末尾から可聴開始までの推定値を「返答開始 約N.N秒」と現在sessionの画面へ表示します。無音frame、受領表示、過去turnの値は表示根拠にしません。

標準live turnは、raw audioをCloud Runから`us-central1`のVertex AI Native Audioへstreamし、音声とcaptionを受け取ります。GA endpointは一度のsetupで応答modalityを一つだけ受け付けるため、`responseModalities`には`AUDIO`だけを指定し、captionは`inputAudioTranscription` / `outputAudioTranscription`を有効化して受け取ります。`TEXT`を応答modalityへ併記しません。最終入力captionの確定前には生成音声を解放せず、Cloud Run内の決定論的なPII・高リスク・tool要求screenを通過した時だけ利用者へ送ります。このscreenはregional DLPでも、Vertex AIへ送信する前の原音検査でもありません。

本人が相手から聞かれた質問について回答支援を明示的に頼んだ場合、初回turnはNative Audioの最終入力captionをCloud Runで決定論的に検査し、生成済みのNative音声を破棄します。対象条件を満たす新規scopeでは、回答本文や質問本文を入力に取らないQ-ARCが有限template IDとslotを選び、監査済みrendererがopen-slot cueへ変換します。繰り返し同一になった暫定captionからこのdecisionとstreaming TTSをprivate commit buffer内で先行できますが、空白だけを正規化した候補byte列がfinal captionと完全一致し、browser commitが確定し、同じ報告質問へ用途分離HMACで束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointを発行した後だけPCMを解放します。汎用scope、汎用checkpoint、cached cueへ音声を結び付けません。不一致なら先読みした状態とPCMを破棄してfinal captionを一度だけ処理します。それ以外はNativeのfinal input captionを監査済みの文字列plannerへ直接渡すため、同じ原音を東京リージョンSTTへもう一度通しません。回答が保留中の継続turnも通常どおりNative Audioでcaptionを確定し、そのfinal captionを監査済み段階controllerへ直接donateします。継続コーチのために同じ原音を東京リージョンSTTへ二重通過させません。監査済みplanner経路では入力内の報告質問span、operator、required slot、確定入力中のevidenceを別々の非可逆tagへ束縛するため、同じ主題の別質問や無関係な次turnを回答完了にしません。具体的な質問、回答、逐語録はstateやDBへ保存しません。`complete`または`release`後の次turnから通常のNative Audio応答へ戻ります。通常の視覚表示「ここまで届いています」は受領だけであり、semantic response latencyやQBA Proofへ数えません。ダミーの固定音声によるreceiptは生成も再生もしません。別に表示するQBA Proofは、入力内の同じ報告質問との対応、確定した今回の入力内の全required slot、A-firstを二重検証できたturnだけの判定です。第三者が現実に質問した事実、正答、能力向上、現在の話者、他場面への転移は証明しません。詳細は [QBA Proof設計](docs/answer-ownership-proof.md) を参照してください。

厳格モードとNative Audioを使えない接続fallbackだけが、raw audioから始まる段階的なSTT経路を使います。回答支援は初回・継続ともNative final captionを同じ監査済みplanner/controllerへ直接渡し、2回目のSTTを使いません。runtime PDF uploadはモードを問わず、state decode・STT・モデル推論より前にbackendで一律拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。厳格モードは別のrequest型として束縛し、文字起こしと応答文の両方がCloud Run内の決定論的検査とregional DLPで`clear`になった時だけ後段へ進め、外部検索とcross-turn stateを許可しません。標準モードの回答支援に厳格モードと同じregional DLP保証はありません。どちらもE2EEでも完全なPII除去でもありません。

原音、文字起こし、Native Audioのcaption、モデル応答、研究query・候補はKOTAEのFirestore、Cloud Storage、アプリログへ保存しません。これはクラウド事業者全体の絶対的なゼロ保持保証ではありません。このリポジトリへ提供されたPDFは課題・設計の参照資料であり、公開runtimeで利用できるupload機能ではありません。runtime PDF本文はbackend境界で一律拒否されます。回答支援の原音は`us-central1`のNative Audioでcaptionを確定し、初回はNative出力を破棄します。初回・継続captionはローカルQ-ARCの質問bound state確立、または監査済み文字列planner/controllerへの直接handoffにだけ使い、同じ原音を東京リージョンSTTへ再送しません。Q-ARC自体へ質問本文を渡さず、handoff層で同じ報告質問由来の非可逆tagをstateへ束縛します。回答支援の状態tokenには、入力内の報告質問spanから作ったoperator、required slot、用途分離した質問／回答の非可逆tag、有限の制御メタデータだけをAES-256-GCMで認証暗号化してブラウザメモリへ返し、具体的な質問・回答・逐語録は入れません。verifier-progress writerを有効にしたrolloutでは、現在の質問に対する検証の進行だけを表す5個の固定小数audit-posterior massを追加します。これは本人のretrieval状態ではなく、質問、回答、診断、人物特性も保存しません。QBA Proofのwire値も`none`または固定claimだけで、tagやスコアを公開しません。段階的な標準経路の会話状態も自由文要約を避け、短い意味nodeと制御メタデータだけを認証暗号化して返します。ただし、後者には未検出の機微情報が残る可能性があり、Cloud Runは復号できます。厳格モードでは会話状態自体を返しません。正確な境界は [音声セキュリティ設計](docs/audio-security.md) を参照してください。

開始速度の実装判定は、受領表示や最初のbinary frameではなく、出力deviceへ配置できた最初の有意味PCMだけを使います。current turnを`native-conversation`、`initial-answer-support`、`continuing-coach`、`http-fallback`、`strict-local`の有限routeへ分類し、発話終了からの1秒目標・3秒miss・10秒stallを本文なしの固定schemaで通知します。Web Audioが推定可聴時刻を所有済みでも、発話終了+10秒より前かつ現在から250 ms以内のslotだけを先行確定し、それより遠いslotは推定可聴境界まで成功扱いにしません。3秒missを観測しても、Native再試行はcommitから3秒が経過し、未確定の割り込み候補が解決した後に、音声event 0、Coach checkpointなし、割り込みなしという既存の安全条件を満たす一回だけです。commit+3秒が絶対停止点より後なら再試行せず、発話終了+10秒を優先します。この停止点はlive準備、capture確定、認証、HTTP、decode、予約済み再生をまたぎます。有意味PCMがなければHTTPを中断し、liveを明示失敗させ、10秒より先の予約音源も停止します。無音PCMやcheckpointは再試行を禁止しますがfail-closeは禁止せず、音声またはcheckpointが出たturnを速度のために再送しません。silent finalなどでturnが先に完了・失敗した場合は10秒まで待ちません。

## 構成

```text
apps/client              Rust + Dioxus 0.7 Web/Wasm UI
apps/client/web          ブラウザAPIとFirebase SDKだけを扱うJavaScript境界
crates/audio_core        公開割込み経路のcontent-free音響frame判定
crates/pcm_ring          AudioWorklet内の固定長・zeroizing PCM所有権
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
- 厳格 / Native Audioを使えないfallbackのSTT / TTSと、回答支援で選ばれた短い文のTTSは`asia-northeast1`のリージョナルエンドポイントへ固定する。回答支援は初回・継続ともNative final captionを監査済みplanner/controllerへ直接donateし、同じ原音へ2回目のSTTを行わない
- 厳格モードはSTT文字列と応答文のCloud Run内決定論検査 + regional DLP検査が`clear`の時だけ後段へ進め、失敗時は停止する。標準モードに同じ保証があるとは表現しない
- 標準liveのVertex AI Native Audioは`us-central1`、文字列推論のVertex AIは`global`であり、raw audio、評価APIで置換した文字列、厳格音声で検査済みの文字列や応答が日本リージョン内に限定されるとは説明しない
- runtime PDF uploadは標準・厳格を問わずbackendでstate decode・STT・モデル推論前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する。提供PDFは課題・設計の参照資料であり、公開機能として利用可能とは説明しない
- 状態鍵はSecret Managerで管理し、状態トークンはFirebase UIDへ束縛して15分で失効させる
- 音声履歴、再生履歴、無人の後日再評価、保存音声Vaultは現在の公開経路に実装していない
- Passkeyによるアカウント操作確認は実装したが、話者本人認証ではない。現行VADは同席者・テレビ・合成音声を利用者の声から識別できないため、周囲の声を取り込まない環境で、利用者自身が相手の質問を言い直した時だけ使う
- 端末内の任意長期測定と時点別の生観測表示は実装済みだが、個人内の自己記録であって有効性・因果効果を示す比較試験ではない
- respondent coachingは利用者が前景で開始・継続した`intentional` / `foreground` turnだけで動かし、受動的な`ambient` turnから保留質問を作成・変更・進行・解除しない
- Crossref候補発見を「検証済み」と呼ばない。任意Web巡回、論文本文取得、claim-evidence照合、定期的な自動収集はまだ実装していない
