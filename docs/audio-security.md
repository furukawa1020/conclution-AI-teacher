# 音声セキュリティ設計

## 現在の保証範囲

現在の公開音声経路は「録音を暗号化して長期保存するサービス」ではありません。利用者が明示的に開始したセッション中に、一つの発話を認識して音声で返します。KOTAE側では原音、文字起こし、Native Audio caption、モデル応答を永続化しません。

標準live turnは、raw audioを`us-central1`のVertex AI Native Audioへstreamします。GA endpointのsetupは`responseModalities`に`AUDIO`だけを指定し、captionは`inputAudioTranscription` / `outputAudioTranscription`を有効化して受け取ります。一度に許される応答modalityは一つなので`TEXT`を併記しません。providerが返す音声とcaptionは、最終入力captionの確定とCloud Run内の決定論的なPII・高リスク・tool要求screenが終わるまで利用者へ解放しません。このscreenはregional DLPでも、Vertex AIへ送信する前の原音検査でもありません。本人が外部の質問への回答支援を明示した初回turnはNative出力を破棄します。対象条件を満たす新規scopeでは、質問本文・回答候補を入力に取らない質問拘束済みQ-ARCが有限template IDとslotを選び、監査済みrendererだけがcueへ変換します。安定した暫定captionからdecisionとstreaming TTSをprivate commit buffer内で先行できますが、空白だけを正規化した候補byte列とfinal captionの完全一致、browser commit、同じ報告質問由来の用途分離HMACへ束縛したAES-256-GCM認証暗号stateを含む有限coach checkpointがそろうまで状態・判断・PCMを解放しません。汎用scope、汎用checkpoint、cached cueは使いません。不一致なら先読みした状態とPCMを破棄し、final captionを一度だけ処理します。それ以外はNativeのfinal input captionを監査済み文字列plannerへ直接handoffし、同じ原音をregional STTへ再送しません。保留中の継続turnもNativeでfinal captionを確定し、そのcaptionを監査済み段階controllerへ直接donateします。継続コーチのために同じ原音を東京STTへ二重通過させません。監査済みplanner経路では、実質問から作ったoperator、required slot、非可逆の質問継続tagで次turnを照合し、無関係な発話を回答完了にしません。具体的な質問・答え・逐語録はstateやDBへ保存しません。厳格モードとNative Audioを使えない接続fallbackだけがraw audioから段階経路へ入り、regional STTを一度だけ使います。runtime PDF uploadはモードを問わず、state decode・STT・モデル推論前にbackendで拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。厳格モードはrequestからresponseまで別の型として扱い、文字起こしとモデル応答の両方をCloud Run内の決定論的検査とregional DLPで検査します。`clear`以外、timeout、権限エラー、応答mode不一致は停止し、外部探索とcross-turn stateを許可しません。どちらのモードもE2EEでも完全なPII除去でもありません。

ここでいう「KOTAE側で保存しない」は、KOTAEのFirestore、Cloud Storage、アプリログ、ブラウザのlocalStorageへ会話本文を書かないという意味です。標準liveの音声は発話ごとのrequest dataとしてVertex AI Native Audioへ、raw audioから始まる段階経路ではregional STTへ渡し、KOTAEはrequest終了後に履歴を保持しません。回答支援の初回・継続caption handoffではNative input captionをCloud Run内のplanner/controllerが平文で扱いますが、regional STTは扱いません。一方、処理に必要な平文は端末、Cloud Run、Vertex AI、および該当する段階経路のSpeech-to-Text、Sensitive Data Protection（DLP）、Text-to-Speechから見えます。Firebase Authenticationも認証用アカウント情報を扱います。E2EE、完全な端末内処理、完全なPII除去、メモリフォレンジックに対する消去保証、Google Cloud全体のゼロ保持を意味しません。管理サービス側のデータ利用・ログ条件は公式契約とproject設定を別に確認します。

## データフローと所在地

```text
マイク
  │ 端末RAM: MediaRecorder + VAD
  ▼
Firebase Hosting /api rewrite または固定run.appへのCORS/TLS
  ▼
Cloud Run kotae-api（asia-northeast1）
  ├─ 標準live: raw PCM ──→ Vertex AI Native Audio（us-central1）
  │                                   ├─ 入力caption gate後のPCM + caption ──→ ブラウザ
  │                                   ├─ 対象となる新規回答支援 ──→ Native出力破棄 → ローカルQ-ARC + private streaming TTS
  │                                   ├─ その他の初回回答支援 ──→ final captionを文字列plannerへ直接handoff
  │                                   └─ 継続回答支援 ──→ final captionを段階controllerへ直接donate
  ├─ 厳格 / 接続fallback: raw audio ──→ Cloud Speech-to-Text V2（asia-northeast1）
  ├─ 厳格時のtranscript ──→ Cloud Run内決定論的検査 + Sensitive Data Protection（asia-northeast1）
  ├─ clearな文字列または標準文字列 ──→ Vertex AI（global）: KOTAE Reflex / LAC
  ├─ 標準時に明示したDOI / 新着topic ──→ Crossref REST API
  └─ 応答文 ──→ 厳格時はCloud Run内決定論的検査 + DLP検査 ──→ Cloud TTS（asia-northeast1）
                                                                                └─ MP3 ──→ ブラウザ
  └─ runtime PDF upload ──→ state decode・STT・modelより前に拒否、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄
```

| データ | 処理先 | アプリ側の永続化 | セッション継続に残るもの |
|---|---|---|---|
| マイク音声 | ブラウザ、Cloud Run、標準liveではVertex AI `us-central1`。厳格 / fallbackだけ東京リージョンSTT | なし | なし |
| Native Audio caption / 音声 | Vertex AI `us-central1`、Cloud Run、ブラウザ。明示回答支援の初回Native出力はブラウザへ解放せず破棄し、初回・継続final input captionは質問boundなAES-256-GCM認証暗号stateの確立または監査済みplanner/controllerへの直接handoffに使う | なし | Q-ARC自体へ質問本文を渡さず、発話本文を状態tokenへ入れない |
| STT直後の文字起こし | 厳格 / fallbackのraw audioから始まる段階経路のCloud Run。厳格時だけ東京リージョンDLP。回答支援のcaption handoffでは生成しない | なし | 厳格時は`clear`でなければVertex AIへ進めない |
| Vertex AIへ渡す文字列 | Cloud Run、Vertex AI `global` | なし | 標準時だけ、未検出情報を含み得る短いgraph nodeが入る可能性がある |
| モデル応答文 | Cloud Run、東京リージョンTTS | なし | 原文は状態へ保存しない |
| 合成音声 | Cloud Run、ブラウザ | なし | 再生後は参照を解放する |
| PDF | backendが全モードでstate decode・STT・modelより前に拒否し、受信PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄。Vertex AIへ送らない | なし | なし。提供PDFは課題・設計の参照資料で、runtime機能ではない |
| 明示した研究query | 標準時だけCloud Run、Crossref | なし | DOI、topic、候補をcross-turn stateへ残さない |
| 研究候補 | Cloud Run、ブラウザ | なし | title、DOI、日付、sourceを現在のresponseだけへ返す |
| 会話状態 | 標準時だけブラウザメモリ、次ターンのCloud Run | サーバーDBには保存しない | 通常Native turnは発話本文を含まないlease。回答支援は実質問由来のoperator・required slot・非可逆tagと有限coach制御metadata。verifier-progress writerのrollout時は、現在の質問に対する検証の進行だけを表す5個の固定小数audit-posterior massを追加し、本人のretrieval状態、質問、回答、診断、人物特性は入れない。段階的な標準turnはフィルタ済み意味グラフと有限coach制御metadata。15分TTL。厳格時は空 |
| Passkey credential | authenticator、Cloud Run、Firestore | SHA-256由来document ID、仮名user handle、public credential、sign counter等。秘密鍵はなし | 仮名アカウント。ceremonyは5分・単回利用 |
| 長期測定 | 明示参加した端末のlocalStorage | 端末内のみ | 有限回答、1〜5、日単位の測定日、無作為な端末内ID、同意・schema version。会話本文・音声・Firebase UID・自由文・時刻は含めず、168日期限を次回アクセス時にprune。全削除後は回答復活防止用の固定markerだけを残す |
| 音声レート制限 | Firestore | 48時間TTL | UIDまたはFirebase App IDのSHA-256由来document IDと回数・時刻 |
| Passkeyレート制限 | Firestore | 48時間TTL | App Check tokenまたは仮名UID由来のclient digestと、App ID由来の高位circuit-breaker digest、回数・時刻。raw token、UID、IPは保存しない |
| live接続lease | Firestore | 最長7分TTL | SHA-256化UID、ランダム所有者、期限だけ。同じ仮名アカウントのlive接続を1本へ制限し、音声・文字起こし・raw UIDは保存しない |

raw audioから始まる段階経路のSTT、該当するDLP、文字列plannerのTTSは`asia-northeast1`のリージョナルAPIエンドポイントへ固定しています。回答支援の初回・継続caption handoffは2回目のSTTを使いませんが、選ばれた短い応答文には東京リージョンTTSを使い得ます。標準liveのraw audioとNative Audio応答はVertex AI `us-central1`、段階経路の文字列推論はVertex AI `global`で処理します。したがって、音声や推論対象が日本国内だけで処理されるとは保証しません。

段階的な経路のSTTは`asia-northeast1`・`ja-JP`の`long`だけを使います。自然な会話の途中の短い間を文末と誤認しにくい会話向けlong-form modelを選び、端末側VADとの一致をcommit条件にしてproviderの判定だけで発話を確定しません。STTのIAM拒否、model利用不可、timeout、decode失敗はすべてfail-closedにし、別modelや東京域外へ自動退避しません。

Cloud Speech-to-Textのstreaming requestは公式上最大5分です。KOTAEはその境界まで使わず、端末では録音開始から最大3分30秒、Cloud Runでは受信開始から4分（20 ms PCMを最大12,000 frame、7,680,000 byte）で止めます。録音開始後に発話が確定しない無音候補は最大30秒で終了するため、その上限直前から話し始めた場合にも残りは約3分あります。ただし、無音や間も端末の3分30秒へ含まれ、3分30秒の実発話を保証するものではありません。provider上限まで60秒を残し、providerのendpoint通知は助言に留め、端末VADとの一致なしにcommitしません。commit後に得た最終文字起こし全体が160 Unicode code point以上の場合だけ、PII検査後の現在turn内で意味を変えない中心点の足場を使えます。これは長期効果や技能を判定する機能ではなく、途中候補、過去turn、保存済み本文から中心点を作りません。

端末VADが音声を確認した後、最後のvoiced frameから700 ms無音が続いた時は、`kotae:voice-receipt`の固定enumだけで「ここまで届いています」を視覚表示します。発話が再開すれば表示を消し、文字起こし、要約、理解判定、confidence、発話本文はeventへ入れません。ダミーの固定音声によるreceiptは生成も再生もしません。通常Native、初回回答支援、継続Coachを別系列にし、commitまたはspeech-endから最初の実質音声frameまで1,000 ms以内を運用SLOとして記録します。現行のroute metadataでは初回Q-ARCと初回caption handoffを互いに分離できないため、この二つは「初回回答支援」として合算し、別系列の実測とは表示しません。視覚表示や意図的な無音`wait`は達成扱いにしません。1秒はブラウザのevent loop、回線、managed modelを含む絶対上限の保証ではありません。利用者は「ここで返して」で長い終端待ちを明示的に飛ばせます。

live WebSocketには、WebSocket upgrade前からGo側で6分の外側deadlineを置きます。4分のcapture deadlineと、commit時点から始まる最大50秒のモデル・TTS処理deadlineは別です。長く話した時間をモデル処理時間へ加算せず、逆に長いcaptureでcommit後の処理枠を先食いもしません。検証済みlive routeだけ接続deadlineを延長し、通常routeはread/write/idle各120秒を維持します。Cloud RunではWebSocketもrequest timeoutの対象なので、service側はGoの6分より1分長い`--timeout=420`へ固定します。アプリがdeadline処理する前に基盤側が接続を切る競合を避けるためです。

未認証のlive handshakeが長時間resourceを占有しないよう、Cloud Run instanceごとにnon-blockingな2 slotのgateをWebSocket upgrade前に置きます。2 slotが使用中ならupgradeせずHTTP 429を返し、受け入れた接続もupgrade後の最初のframeを2秒以内に要求します。Firebase ID tokenとApp Check tokenの検証が完了した直後に、成功・失敗を問わずslotを解放するため、通常の長いlive sessionはこのslotを保持しません。`--concurrency=4`に対し、gateが待機を許す未認証handshakeは最大2 request、つまりinstance内concurrencyの最大1/2です。これにより未認証handshakeだけでは残り2 request slotを占有できません。これはinstance内のresource占有を制限する層であり、Cloud Run前段のedge DDoS防御でも、credentialを一回しか使えないticketへ変える仕組みでもありません。

UID leaseを取得してpipelineを開始した後に接続切断やdeadlineへ達した場合は、まずpipelineのcontextをcancelし、終了signalを最大5秒待ちます。終了を確認できた時だけ所有者照合付きtransactionでleaseを解放します。5秒以内に終了しない時は即時解放せず、7分の`expiresAt`までleaseを保持して、停止確認できない旧pipelineと同じUIDの新pipelineを重ねにくくします。これはprovider側の停止を暗号学的に証明する仕組みではなく、instance消失、Firestore障害、ネットワーク攻撃を完全に防ぐ保証でもありません。

## マイクとセッション制御

- 最初のタップを明示的な開始操作とし、開始前はマイクを取得しない
- 各requestは`turnMode: intentional | foreground | ambient`を必須とし、状態tokenの有無から権限を推測しない。foregroundは返答を期待するが、外部作用や状態更新についてはambientと同じ制限を保つ
- 端末側VADは発話区間を決めるためだけに使い、声紋認証、感情診断、病気や性格の推定に使わない
- AI処理中と合成音声の再生中も、利用者が開始した会話セッション内では訂正・割り込みを受けるためマイクトラックを有効にする。端末内VADが確認する前の音声は送信せず、確認した割り込みだけをForeground turnとして送る
- 確認前PCMはAudioWorklet内だけに保持し、短いぼやきや相づちは再生を変えず160 msで端末内の仮候補に留める。明瞭・静音のどちらも1,200 ms以上の持続音声、foreground density 0.72以上、候補全体のvoice density 0.68以上を満たした時だけ応答を中断する。候補は100 ms pre-rollを含む固定長ringへ最大2,500 ms相当を保持し、未確認のPCMを端末外へ出さない。VAD確認後はAudioContextのsample-clock cutoff、session generation、連続sequenceを検証し、credit制御でMessagePortの未処理数も固定する。turn確定は全PCMの`sealed`確認後だけ許可する
- 割り込み待機を含むセッション全体を4分の無発話または30分の絶対上限で終了し、期限時は通信、PCMリング、録音、再生、マイクトラックを同じepochで破棄する。4分は会話時間の目標ではなく安全上の仮上限で、一往復や数秒で終えてよい。idle時計は発話確認時に更新されるため、録音開始から最大3分30秒の単一turn captureと5秒の終端待ちより後へ固定する。ただし検証済み応答の生成・再生中は4分のidle判定だけを保留し、30分の絶対上限は維持する
- タブが非表示になった時と`pagehide`時に録音と再生を止め、マイクトラックを解放する
- 応答を最後まで再生した時点から次の4分を数え直す。ページ非表示、`pagehide`、マイク喪失は応答中でも直ちに停止する
- 一turnは端末側で録音開始から最大3分30秒とし、発話が確定しないままの無音候補は最大30秒とする。12秒以上続いた明確な独話では最後の音声から5秒の無音を待つ。30秒の上限直前から話し始めても約3分は残るが、無音や間も3分30秒へ含まれるため、3分30秒の実発話を保証しない。live PCMはCloud Run側で4分・12,000 frame・7,680,000 byteを上限とし、Base64・状態token・JSONを含むrequest envelopeは13 MiBを上限にする。圧縮音声のHTTPS fallbackは2 MiBで、codecとbitrateがブラウザごとに異なるため長時間発話を通せる保証はない。2 MiBを超えた時は保持中の全chunkを破棄し、先頭だけのpartial audioをuploadしない。runtime requestに`document`があればモードや宣言サイズにかかわらず内容を使わず拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する
- 会話状態はJavaScript変数にだけ保持し、localStorageへ保存しない。Passkey由来のFirebase Auth sessionだけは`browserSessionPersistence`を使う。長期測定は別の明示opt-in ledgerとして有限値だけをlocalStorageへ保存する

JavaScriptのガベージコレクションや文字列の複製は完全には制御できません。クライアントは使用後に参照を解放し、Go側は受信byte sliceを可能な範囲でclearしますが、これを暗号学的なRAM消去保証とは表現しません。

現在のVADは発話区間だけを見ており、話者本人認証ではありません。同席者、テレビ、合成音声を利用者本人だと安全に識別する機能もありません。そのため公開UIは、周囲の質問を常時取り込む使い方ではなく、利用者自身が「こう聞かれた」と質問を言い直してから回答を話す使い方に限定して案内します。

Passkey登録・認証では、WebAuthnのresident credentialとuser verificationを必須にし、RP ID、exact origin、challenge、5分期限、単回利用をサーバーで検証します。初回登録はFirebase App Checkを通過した公開アプリから明示操作で開始でき、検証後に仮名Firebase account用custom tokenを発行します。音声APIは`kotae_account_verified=true`、`kotae_authn=passkey-v1`、署名検証時刻`kotae_passkey_at`を持つID tokenを検証します。custom tokenの交換時刻`auth_time`だけをfreshness根拠にしないため、遅延交換や再交換で5分境界を延命できません。確認できるのは登録済みauthenticatorによるアカウント操作であり、自然人の法的身元、端末の唯一の所有者、現在マイクで話す人までは証明しません。

厳格モードでは、文字起こしと応答文をcredential・連絡先などのCloud Run内決定論検査とregional DLPの検査へ通し、両方が明示的に`clear`の時だけ次のクラウド処理へ進めます。DLPがtimeout、権限拒否、構造不正を返した場合は元の文字列へfallbackしません。streaming合成音声もrequest-boundな`clear`検証までサーバー内の上限付きbufferへ保持し、blocked/error/mode不一致ならwireへ出さずzeroingします。ただし、自然言語中の氏名、珍しい識別子、文脈から特定できる情報を漏れなく検出できる保証はありません。標準モードにはこのstrict boundaryを適用していません。この境界は低減策であり、raw audioを扱うSTTを含む完全PII除去やE2EEではありません。

## 研究queryの境界

- 外部探索は、現在のintentional turn全体が「外部検索で、テーマは何々の最新論文を探して」または「Crossrefで DOI … を調べて」という固定形式に完全一致した場合だけ。自然文から同意を推測せず、追記、取消し、複数命令、ambient turnには外部送信権限を与えない
- `assistant`経路だけで許可し、本人回答の`respondent`経路から外部queryを作らない
- topicは固定形式の「テーマは」と「の最新論文」の間全体、DOIは空白で区切ったbare DOI全体だけを使い、PDF、過去state、別文、推測した個人情報から作らない。発話から決定論的に抽出した値、モデルの値、取得結果を完全一致で結ぶ
- `@`を含む値、電話番号らしい値、郵便番号、長い番号、ASCII外の数字、API key、token、credential assignment、既知の氏名形や患者・健康文脈らしいqueryを送信前に保守的に拒否する。HTML entity、percent encoding、PIIらしいBase64・Base32文字列も復号して元の文へ戻し、分割されたBase64候補も結合して再検査する
- 送信値はNFKCで表記が変わる文字とUnicode format文字をfail-closedで拒否する。検査時にはformat文字を空白へ置換した形と除去した形の両方も確認し、全角`＠`、全角数字区切り、ゼロ幅文字による検査分断を許可しない。DOIのcanonical suffixはASCIIへ限定する
- topicに許可する文字をUnicodeの文字・結合記号、ASCII数字、空白、hyphen、数字間のdecimal pointへ限定する。任意URL、slash、colon、`@`、一般の記号をtopicとして送らない
- topicに句読点・節区切り・取消語が入った場合と、DOIにcomma・semicolon・取消語が付いた場合は送信しない。「量子、いや、やめて」や`10.1234/public,cancel`を検索対象として吸収しない
- 接続先は`https://api.crossref.org`へ固定し、redirectを拒否する
- Crossref source requestは8秒、会話側tool-policyは7秒で打ち切り、responseは2 MiB、返却候補は5件を上限にする
- abstractはcopyright状態が不明なため、音声応答や画面へ再配布しない
- 結果は常に`discovery_metadata_not_claim_evidence`かつ`needs_primary_evidence`とし、「検証済み」というstatusを型として持たない
- 現在のtopic探索はCrossrefのindex date filterを使うため、「新しく発表された論文」ではなく「Crossrefの索引日が指定期間内の書誌候補」と表示する

このscreenは完全なPII検出ではありません。未知の研究語をclosed vocabularyで拒否すると本機能の目的を壊すため、任意の語が氏名か新規技術名かを完全には判定しません。固定形式で発話したtopicそのものがCrossrefへ送られることを、初期表示したprivacy欄に示し、氏名・連絡先・症例を入れないよう案内します。意味的に機微なtopicや氏名をすべて検出できるとは保証しないため、任意Web巡回、PDF由来の自動query、バックグラウンド定期収集は現在の公開経路へ接続しません。

## API境界

`POST /api/v1/voice/turns`では次をすべて要求します。

- Passkey検証由来claimを持つFirebase ID token。アカウント操作の確認であり、話者本人認証ではない
- Firebase App Check token
- 許可済みFirebase App ID
- 完全一致する`Origin: https://kotae-ai.web.app`。missing、`null`、別origin、重複Originを拒否
- `application/json`と許可済み音声MIME
- サイズ上限、strict Base64、未知JSON fieldの拒否
- JSON本文とBase64を読む前に消費するUID単位とFirebase App単位の二段階レート制限
- buffered requestは個別のrequest timeout、live WebSocketは6分の外側deadlineを持つ。liveのモデル処理deadlineはcommit時点から別に開始する

段階的な経路の`caption`は実際にTTSへ渡した最終`SpokenReply`だけで、Native Audioの`caption`は実際に解放したprovider音声の出力captionだけです。入力文字起こし、内部推論、LAC本文は返しません。意図的な沈黙では音声を空、`caption`を`null`にします。

runtimeの`document`は標準・厳格を問わず受け付けません。backendは`document`を含むrequestをstate decode・STT・モデル推論前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。提供されたPDFは課題・設計を合わせるための参照資料であり、公開runtimeで選択・送信できる機能ではありません。

## 会話状態

会話履歴をサーバーへ保存する代わりに、短い意味状態を不透明なtokenとしてブラウザへ返します。

- AES-256-GCM
- ランダムnonce
- Firebase UIDをAADへ含め、別ユーザーへの差し替えを拒否
- 発行から15分で失効
- schema、長さ、turn数を復号後にも検証
- 暗号鍵は32 byteで、Cloud RunへSecret Managerから注入
- tokenへ逐語録、会話・資料の自由文要約、PDF本文、モデルのchain-of-thoughtを入れない
- `extended_speech`の今回限りの判定値と逐語録・発話本文はtokenへ入れない。一方、長い発話も通常会話と同じ状態更新の対象であり、PII検査と決定論的フィルタを通した抽象化済み・件数と長さに上限のあるgoal、claim、ground、assumption、constraint、open loop、contradiction、decisionは15分tokenへ残り得る
- graph nodeはemail、電話番号、長い数列、credentialらしいtokenを含む場合、または現在発話との4-gram重複が高い場合にnodeごと破棄する
- 本人へ一度だけ言い直しを頼んだ場合は、target evidenceを含む正規化意味節本文ではなく、状態暗号鍵から用途分離して導出した秘密鍵によるHMAC-SHA-256の128 bit tagだけをtokenへ入れる。MAC inputを暗号化session IDへも束縛する。tagは`awaiting_restatement`以外では拒否し、planner、critic、TTS、response metadata、ログへ渡さない
- 次のtarget evidenceがtagと一致しなければ、plannerとcriticが成功を申告しても完了扱いにしない。再回答を強制せず、保留scopeを消して通常会話へ戻す
- tag発行を有効化したrevisionは、互換revision由来のtagなし`awaiting_restatement` scopeを推論前に消す。tokenを繰り返し更新して未束縛scopeを延命できない
- KOTAE自身が生成した任意質問は新しい採点scopeを作らない。以前の15分tokenにあるlegacy fieldはstrict decode後、model inferenceより前にscopeごと消し、新規には発行しない

tokenにはフィルタ済みでも会話由来の意味nodeが含まれ得るため、秘密でないデータとは扱いません。また、Cloud RunはSecret Managerの鍵を使って復号できるためE2EEではありません。秘密へのアクセスはCloud Runの実行サービスIDだけに限定します。

HMAC tagも会話由来のpseudonymous control dataであり、完全なPII除去とは呼びません。現在のtagは言い直し前後でtarget evidenceを含む意味節が変わっていないことだけを保守的に検査します。質問topicとの意味関係、同義な言い換え、話者本人性は証明しません。target意味節が変わった場合は正しい言い換えでも完了creditを与えないことがありますが、その場合も再試験ではなく通常会話へ解放します。

## PDFのruntime境界

公開runtimeではPDF uploadを全モードで無効にします。backendへ`document`が直接送られた場合も、state decode・STT・モデル推論より前にfail-closedで拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄します。PDF本文、ファイル名、資料要約を状態、ログ、モデル入力へ渡しません。

このリポジトリへ提供されたPDFは「Aと聞かれてAと答えられない」という課題を定義し、設計を照合するための参照資料です。公開製品で利用可能な添付機能を意味しません。将来file inputを検討する場合は、de-identification境界、明示同意、処理先、保持、削除、prompt injectionを改めて審査する必要があります。任意URL取得、PDFからの自動query、PDF原本の完全PII除去も実装していません。

## ログと永続データ

原音、文字起こし、モデルprompt/response、PDF本文、Firebase token、App Check token、状態token、秘密鍵をKOTAEのアプリログへ出しません。この記述は管理されたGoogle Cloudサービス全体のログ・保持条件を一括で保証するものではありません。音声APIの運用ログは次に限定します。

- request ID
- fast / precisionなどのroute
- 音声を返したかどうか
- 処理時間
- 列挙したerror classと、rate-limit障害時の`uid` / `app`というscope区分

既存の`/api/v1/evaluations`は評価メタデータを30日TTLで保存しますが、音声会話経路はこのevaluation storeへ本文を書きません。音声レート制限counterは別collectionへ保存し、48時間TTLを設定します。

## 推論と過剰介入の安全策

KOTAE ReflexとLatent Answer Contract（LAC）はプロジェクト独自の実験的な制御機構であり、医療機器や安全認証済みの判断機構ではありません。

- 潜在問いの上位候補が近く曖昧なら、勝手に一つへ固定せず確認または沈黙を選ぶ
- 最終draftの後に、draft側のLAC自己申告を渡さない低遅延fast modelの別structured callで回答を監査する。これは別モデル監査ではなく、隔離promptと別callによる独立監査である。構造不正または一時的provider障害が二度続いた時だけprecision modelで一度回復監査し、安全終了・cancelでは回復を試みない
- 答えが問いの必須slotを満たすか、最初のコミットメントがどこにあるかを決定論的に再検証する
- first commitmentが候補回答内に実在することと、keepに必要な必須slotの完全充足を決定論的に確認する
- 再構成で条件、因果、boolean極性、数値・単位、選択肢label、引用anchor、不確実性が変わる場合は、その修復案を拒否する
- 自己修正の兆候がある時は、AIの訂正より本人の言い直しを優先する
- 日常のぼやきや感情表現を、常に論理誤りとして矯正しない
- runtime PDF uploadは全モードで推論経路へ入る前に拒否する。Native Audioで医療、法律、金融、研究・tool要求を決定論的に検出した時はprovider出力を解放せず、まだ音声を一切返していない同一turnだけを明示sentinelで段階的な精密経路へ再送する。段階的な経路でも高リスクとして扱い、精密経路が使えない時は実質回答を読み上げない
- STTが0より大きく0.65未満のconfidenceを返した場合、文字起こしをモデルへ渡さず、intentionalなら固定文で一度だけ聞き返し、foregroundと受動ambientは沈黙してマイクを閉じる。confidence 0はAPIが値を提供しなかった状態として扱い、低信頼判定とは区別する

`0.65`は未校正の補助境界であり、誤認識をゼロにする保証ではありません。Google Cloudが返す値を真の確率とはみなさず、実利用条件の音声でROC、聞き返し率、取りこぼし率を測って校正する必要があります。

LACの`Target Slot Coverage`、`Commitment Front Position`、`Meaning Preservation`は内部の制御・評価指標であり、モデルの自己申告だけを正解とはしません。現在は研究的な仮説であり、実際の会話データによる精度、誤介入率、校正の検証が必要です。

## 保証しないこと

- 端末内だけの処理
- E2EE
- 完全なPII除去
- 声紋による本人確認
- Passkey確認を、自然人の法的本人確認や現在の話者認証とみなせること
- 音声と、評価APIで置換した文字列または厳格音声で検査済みの文字列が、すべて日本リージョン内に留まること
- 第三者クラウドを含む絶対的なゼロデータ保持
- ブラウザ拡張、OSマルウェア、画面・スピーカーの盗み見への防御
- モデル回答の正しさ、最新情報の自動保証
- 会話支援による回答能力や生活状態の長期的な改善
- 保存音声の履歴、再生、共有、後日再評価

`crates/audio_vault`は将来の同意制履歴を検討するための暗号化コアで、現在の公開音声経路には接続していません。履歴機能を追加する場合は、raw audio保存、応答音声保存、後日再評価、共有、品質改善を別々に同意させ、保存先、削除、鍵管理、監査を改めて設計します。

## 最低限の検証

- 必要なPasskey claimを持たないID token、App Check、Originのどれかが不正ならモデルを呼ばない
- missing / unknown `turnMode`を拒否し、intentional・foreground・ambientを状態tokenから推測しない
- 不正本文でも認証後はUID枠とApp枠が先に消費される
- 未知field、不正MIME、過大音声を拒否する。runtime PDFは全モードでAPIのstate decode・STT・モデル推論前に拒否し、PDF payloadをbase64 decodeせず下流へ渡さず、request終了時に参照を破棄する
- 測定されたSTT confidenceが低い時に文字起こしがモデルへ届かない
- 厳格時にlocal PII検査またはregional DLPが失敗したらraw transcriptがVertex AIへ届かず、応答検査が失敗したらTTS音声がwireへ届かず、同じ内容の言い直しも要求しない
- 状態tokenの改ざん、期限切れ、別UIDでの利用を拒否する
- 曖昧な潜在問いで断定的な再構成をしない
- 条件、不確実性、留保を変える再構成を拒否する
- draft自身のLACを偽装しても、独立監査と決定論的判定を迂回できない
- plannerとcriticが別のAを成功と申告しても、言い直し前のserver-only HMAC tagと不一致なら完了できない。tag自体が両model promptへ入らない
- AI自身の任意質問から、利用者が頼んでいない採点scopeを作らない
- 160 Unicode code point未満の最終文字起こしや途中候補から長い独話用の中心点足場を作らない。160以上でも中心点の足場は現在turnの意味保存にだけ使い、`extended_speech`の判定値、逐語録・発話本文、技能判定をcross-turn stateへ残さない。ただし、通常会話と同じ状態更新により、検査・フィルタ後の抽象化済み・有限化されたgoal、claim、open loopなどは暗号化された15分stateへ残り得る
- どのモードでもrequestにPDFを付けた場合はstate decode・STT・モデルを呼ばない。高リスク発話で精密経路が停止した時も、高速draftの実質回答を返さない
- 自己修正中と介入価値が低い発話では沈黙する
- タブ非表示、pagehide、現在の仮上限である4分の無発話、30分の絶対上限、マイクtrack喪失でマイクを解放し、内容を含まない固定理由だけを通知してPausedへ移る。4分滞在を要求せず、利用者は一往復でも終了できる。Rust側は固定reason・version以外の通知を拒否し、通知を受けても停止処理を冪等に再実行してからPausedを表示する
- Pausedでは暗号化済みsession stateを消さず、明示的な再開操作だけがIntentionalとなる。Foreground再待受は既存のlive trackだけを再利用し、別マイクを自動取得しない
- 30秒の空captureと認証済みSTT no-speechはForegroundで再待受し、Intentional権限を継承しない。確定発話の送信失敗を自動再送しない
- 通常発話は1.2秒、1.6秒以上の発話は2.2秒の間を待つ。12秒以上続いた明確な独話だけ5秒へ延ばし、短い質問の確定待ちは増やさない。約3分の連続発話を、録音開始から最大3分30秒という端末上限とserverの4分上限の内側で処理する
- 確定文字起こしが160 rune以上の時だけ、`extended speech`を現在turnの主点反射・構成に使う。分類も本文もcross-turn stateへ残さず、3分話せることを長期的な会話能力向上の証拠とは扱わない
- 圧縮音声fallbackが2 MiBを超えた時は全chunkを破棄し、切れた音声をuploadしない。fallbackを約3分の長時間保証として扱わない
- 4分のcaptureとcommit後最大50秒の処理がGo live 6分deadline内に収まり、Cloud Run request timeoutが420秒である
- 未認証live handshakeはinstanceごとのnon-blockingな2 slotだけへ入り、満杯時はupgrade前に429となる。`--concurrency=4`の最大1/2に抑え、受け入れ後も最初のframeを2秒で打ち切り、認証検証完了時にslotを解放する
- 同じFirebase UIDのlive接続は、音声受信前にFirestoreの短命leaseを取得して同時に1本へ制限する。接続終了時はpipelineをcancelして終了を最大5秒待ち、終了を確認した時だけ所有者照合付きでUID leaseを解放する。終了未確認またはinstance消失時は7分TTLまで保持する
- ログ、Firestore、Cloud Storageへ音声、逐語録、PDF本文が作られない

参考:

- [Cloud Speech-to-Text V2の対応モデルと地域](https://cloud.google.com/speech-to-text/docs/speech-to-text-supported-languages)
- [Cloud Speech-to-Textのquotaと5分streaming上限](https://cloud.google.com/speech-to-text/docs/quotas)
- [Speech-to-Text data usage FAQ](https://cloud.google.com/speech-to-text/docs/data-usage-faq)
- [Cloud Text-to-Speech Chirp 3 HD](https://cloud.google.com/text-to-speech/docs/chirp3-hd)
- [Text-to-Speech regional endpoints](https://cloud.google.com/text-to-speech/docs/endpoints)
- [Text-to-Speech data logging](https://cloud.google.com/text-to-speech/docs/data-logging)
- [Vertex AI zero data retention](https://cloud.google.com/vertex-ai/generative-ai/docs/vertex-ai-zero-data-retention)
- [Sensitive Data Protectionの検出精度に関する注意](https://cloud.google.com/sensitive-data-protection/docs/infotypes-reference)
- [Sensitive Data Protectionの処理ロケーション](https://cloud.google.com/sensitive-data-protection/docs/locations)
- [Web Authentication Level 3](https://www.w3.org/TR/webauthn-3/)
- [Firebase custom token](https://firebase.google.com/docs/auth/admin/create-custom-tokens)
- [Firebase App Check custom backend](https://firebase.google.com/docs/app-check/custom-resource-backend)
- [Secret Manager access control](https://cloud.google.com/secret-manager/docs/access-control)
- [Cloud Run service identity](https://cloud.google.com/run/docs/securing/service-identity)
- [Cloud RunのWebSocketとrequest timeout](https://cloud.google.com/run/docs/triggering/websockets)
- [Cloud Run request timeoutの設定](https://cloud.google.com/run/docs/configuring/request-timeout)
