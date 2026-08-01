# 音声セキュリティ設計

## 現在の保証範囲

現在の公開音声経路は「録音を暗号化して長期保存するサービス」ではありません。利用者が明示的に開始したセッション中に、一つの発話を認識し、必要なら音声合成します。KOTAE側では原音、文字起こし、モデル応答を永続化しません。

標準モードは一ターン限りのPDF、明示したCrossref探索、15分の暗号化会話状態を利用できます。厳格モードはrequestからresponseまで別の型として扱い、raw audioをregional STTへ渡した後の文字起こしとモデル応答の両方をlocal検査とregional DLPで検査します。`clear`以外、timeout、権限エラー、応答mode不一致は停止し、PDF、外部探索、cross-turn stateを許可しません。どちらのモードもE2EEでも完全なPII除去でもありません。

ここでいう「KOTAE側で保存しない」は、KOTAEのFirestore、Cloud Storage、アプリログ、ブラウザのlocalStorageへ会話本文を書かないという意味です。音声は発話ごとのrequest dataとしてregional STTへ渡し、KOTAEはrequest終了後に履歴を保持しません。一方、処理に必要な平文は端末、Cloud Run、Speech-to-Text、Sensitive Data Protection（DLP）から見えます。Firebase Authenticationも認証用アカウント情報を扱います。E2EE、完全な端末内処理、完全なPII除去、メモリフォレンジックに対する消去保証、Google Cloud全体のゼロ保持を意味しません。管理サービス側のデータ利用・ログ条件は公式契約とproject設定を別に確認します。

## データフローと所在地

```text
マイク
  │ 端末RAM: MediaRecorder + VAD
  ▼
Firebase Hosting /api rewrite
  ▼
Cloud Run kotae-api（asia-northeast1）
  ├─ raw audio ──→ Cloud Speech-to-Text V2（asia-northeast1）
  │                    └─ transcript
  ├─ 厳格時のtranscript ──→ local検査 + Sensitive Data Protection（asia-northeast1）
  │                           ├─ clear以外 ──→ Vertex AIを呼ばず固定の安全終了
  │                           └─ clear ──→ Vertex AI（global）
  │                                                   ├─ KOTAE Reflex / LAC
  │                                                   └─ silence または短い応答文
  ├─ 標準時に明示したDOI / 新着topic ──→ Crossref REST API
  │                                  └─ 書誌候補（claim evidenceではない）
  └─ 応答文 ──→ 厳格時はlocal + DLP検査 ──→ Cloud Text-to-Speech（asia-northeast1）
                    └─ MP3 ──→ ブラウザ再生
```

| データ | 処理先 | アプリ側の永続化 | セッション継続に残るもの |
|---|---|---|---|
| マイク音声 | ブラウザ、Cloud Run、東京リージョンSTT | なし | なし |
| STT直後の文字起こし | Cloud Run。厳格時だけ東京リージョンDLP | なし | 厳格時は`clear`でなければVertex AIへ進めない |
| Vertex AIへ渡す文字列 | Cloud Run、Vertex AI `global` | なし | 標準時だけ、未検出情報を含み得る短いgraph nodeが入る可能性がある |
| モデル応答文 | Cloud Run、東京リージョンTTS | なし | 原文は状態へ保存しない |
| 合成音声 | Cloud Run、ブラウザ | なし | 再生後は参照を解放する |
| PDF | 標準時だけCloud Run、Vertex AI `global` | なし | 一ターン後にブラウザ参照を解放。厳格時は読込・送信しない |
| 明示した研究query | 標準時だけCloud Run、Crossref | なし | DOI、topic、候補をcross-turn stateへ残さない |
| 研究候補 | Cloud Run、ブラウザ | なし | title、DOI、日付、sourceを現在のresponseだけへ返す |
| 会話状態 | 標準時だけブラウザメモリ、次ターンのCloud Run | サーバーDBには保存しない | フィルタ済み意味グラフと制御メタデータ、15分TTL。厳格時は空 |
| Passkey credential | authenticator、Cloud Run、Firestore | SHA-256由来document ID、仮名user handle、public credential、sign counter等。秘密鍵はなし | 仮名アカウント。ceremonyは5分・単回利用 |
| 長期測定 | 明示参加した端末のlocalStorage | 端末内のみ | 有限回答、1〜5、日単位の測定日、無作為な端末内ID、同意・schema version。会話本文・音声・Firebase UID・自由文・時刻は含めず、168日期限を次回アクセス時にprune。全削除後は回答復活防止用の固定markerだけを残す |
| 音声レート制限 | Firestore | 48時間TTL | UIDまたはFirebase App IDのSHA-256由来document IDと回数・時刻 |
| Passkeyレート制限 | Firestore | 48時間TTL | App Check tokenまたは仮名UID由来のclient digestと、App ID由来の高位circuit-breaker digest、回数・時刻。raw token、UID、IPは保存しない |

STT、DLP、TTSは`asia-northeast1`のリージョナルAPIエンドポイントへ固定しています。一方、意味推論に使うVertex AIのロケーションは`global`です。したがって、raw audioはSTT境界では東京リージョンで処理されますが、検査・置換後の文字列と応答文まで日本国内に限定されるとは保証しません。

STTは`asia-northeast1`・`ja-JP`の`long`だけを使います。自然な会話の途中の短い間を文末と誤認しにくい会話向けlong-form modelを選び、端末側VADとの一致をcommit条件にしてproviderの判定だけで発話を確定しません。STTのIAM拒否、model利用不可、timeout、decode失敗はすべてfail-closedにし、別modelや東京域外へ自動退避しません。

## マイクとセッション制御

- 最初のタップを明示的な開始操作とし、開始前はマイクを取得しない
- 各requestは`turnMode: intentional | foreground | ambient`を必須とし、状態tokenの有無から権限を推測しない。foregroundは返答を期待するが、外部作用や状態更新についてはambientと同じ制限を保つ
- 端末側VADは発話区間を決めるためだけに使い、声紋認証、感情診断、病気や性格の推定に使わない
- AI処理中と合成音声の再生中も、利用者が開始した会話セッション内では訂正・割り込みを受けるためマイクトラックを有効にする。端末内VADが確認する前の音声は送信せず、確認した割り込みだけをForeground turnとして送る
- 確認前PCMはAudioWorklet内だけに保持し、通常発話は最大25 frame、割り込みは最大20 frameに固定する。VAD確認後はAudioContextのsample-clock cutoff、session generation、連続sequenceを検証し、credit制御でMessagePortの未処理数も固定する。turn確定は全PCMの`sealed`確認後だけ許可する
- 割り込み待機を含むセッション全体を、現在は最大4分の無発話または30分の絶対上限で終了し、期限時は通信、PCMリング、録音、再生、マイクトラックを同じepochで破棄する。4分は会話時間の目標ではなく安全上の仮上限で、一往復や数秒で終えてよい。実測で安全に短くできる境界は短くする。ただし検証済み応答の生成・再生中はidle判定だけを保留し、30分の絶対上限は維持する
- タブが非表示になった時と`pagehide`時に録音と再生を止め、マイクトラックを解放する
- 応答を最後まで再生した時点から次の無発話上限を数え直す。ページ非表示、`pagehide`、マイク喪失は応答中でも直ちに停止する
- クライアントの一回の音声captureは最大3分30秒とする。長い独話では自然な考え込みを短い発話の終端と同一視せず、最後の音声から5秒の無音を待ってturnを確定する
- 主経路は認証付きWebSocketへ20 ms単位のPCM frameを増分送信する。サーバーはクライアントとは独立に、captureを最大4分か12,000 frameの先に達した方で停止し、live / HTTPのturn全体は最大6分で終了する
- 同期圧縮HTTP fallbackの音声上限は2 MiBであり、3分級の長時間音声を処理できるとは保証しない。Base64・状態token・JSONを含むrequest envelopeは13 MiB、PDFは7 MiBを上限にし、厳格requestのdocument fieldは内容を読む前に拒否する
- 会話状態はJavaScript変数にだけ保持し、localStorageへ保存しない。Passkey由来のFirebase Auth sessionだけは`browserSessionPersistence`を使う。長期測定は別の明示opt-in ledgerとして有限値だけをlocalStorageへ保存する

JavaScriptのガベージコレクションや文字列の複製は完全には制御できません。クライアントは使用後に参照を解放し、Go側は受信byte sliceを可能な範囲でclearしますが、これを暗号学的なRAM消去保証とは表現しません。

現在のVADは発話区間だけを見ており、話者本人認証ではありません。同席者、テレビ、合成音声を利用者本人だと安全に識別する機能もありません。そのため公開UIは、周囲の質問を常時取り込む使い方ではなく、利用者自身が「こう聞かれた」と質問を言い直してから回答を話す使い方に限定して案内します。

Passkey登録・認証では、WebAuthnのresident credentialとuser verificationを必須にし、RP ID、exact origin、challenge、5分期限、単回利用をサーバーで検証します。初回登録はFirebase App Checkを通過した公開アプリから明示操作で開始でき、検証後に仮名Firebase account用custom tokenを発行します。音声APIは`kotae_account_verified=true`、`kotae_authn=passkey-v1`、署名検証時刻`kotae_passkey_at`を持つID tokenを検証します。custom tokenの交換時刻`auth_time`だけをfreshness根拠にしないため、遅延交換や再交換で5分境界を延命できません。確認できるのは登録済みauthenticatorによるアカウント操作であり、自然人の法的身元、端末の唯一の所有者、現在マイクで話す人までは証明しません。

厳格モードでは、文字起こしと応答文をcredential・連絡先などのローカル決定論的検査とregional DLPの検査へ通し、両方が明示的に`clear`の時だけ次のクラウド処理へ進めます。DLPがtimeout、権限拒否、構造不正を返した場合は元の文字列へfallbackしません。streaming合成音声もrequest-boundな`clear`検証までサーバー内の上限付きbufferへ保持し、blocked/error/mode不一致ならwireへ出さずzeroingします。ただし、自然言語中の氏名、珍しい識別子、文脈から特定できる情報を漏れなく検出できる保証はありません。標準モードにはこのstrict boundaryを適用していません。この境界は低減策であり、raw audioを扱うSTTを含む完全PII除去やE2EEではありません。

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
- request timeout

レスポンスの`caption`は、実際にTTSへ渡した最終`SpokenReply`だけです。文字起こし、内部推論、LAC本文は返しません。意図的な沈黙では音声を空、`caption`を`null`にします。

標準モードの`document`はPDF・7 MiB以下に限定し、利用者が選択した次の一ターンだけVertex AIへinline送信し、応答後にクライアント参照を解放します。本文、ファイル名、要約を状態やKOTAEのDBへ残しません。厳格モードはfile inputを無効にし、橋渡し関数は`File.arrayBuffer()`より前に停止します。APIへ直接`document`を付けた厳格requestもSTT・モデル推論前に拒否します。

## 会話状態

会話履歴をサーバーへ保存する代わりに、短い意味状態を不透明なtokenとしてブラウザへ返します。

- AES-256-GCM
- ランダムnonce
- Firebase UIDをAADへ含め、別ユーザーへの差し替えを拒否
- 発行から15分で失効
- schema、長さ、turn数を復号後にも検証
- 暗号鍵は32 byteで、Cloud RunへSecret Managerから注入
- tokenへ逐語録、会話・資料の自由文要約、PDF本文、モデルのchain-of-thoughtを入れない
- graph nodeはemail、電話番号、長い数列、credentialらしいtokenを含む場合、または現在発話との4-gram重複が高い場合にnodeごと破棄する
- 本人へ一度だけ言い直しを頼んだ場合は、target evidenceを含む正規化意味節本文ではなく、状態暗号鍵から用途分離して導出した秘密鍵によるHMAC-SHA-256の128 bit tagだけをtokenへ入れる。MAC inputを暗号化session IDへも束縛する。tagは`awaiting_restatement`以外では拒否し、planner、critic、TTS、response metadata、ログへ渡さない
- 次のtarget evidenceがtagと一致しなければ、plannerとcriticが成功を申告しても完了扱いにしない。再回答を強制せず、保留scopeを消して通常会話へ戻す
- tag発行を有効化したrevisionは、互換revision由来のtagなし`awaiting_restatement` scopeを推論前に消す。tokenを繰り返し更新して未束縛scopeを延命できない
- KOTAE自身が生成した任意質問は新しい採点scopeを作らない。以前の15分tokenにあるlegacy fieldはstrict decode後、model inferenceより前にscopeごと消し、新規には発行しない

tokenにはフィルタ済みでも会話由来の意味nodeが含まれ得るため、秘密でないデータとは扱いません。また、Cloud RunはSecret Managerの鍵を使って復号できるためE2EEではありません。秘密へのアクセスはCloud Runの実行サービスIDだけに限定します。

HMAC tagも会話由来のpseudonymous control dataであり、完全なPII除去とは呼びません。現在のtagは言い直し前後でtarget evidenceを含む意味節が変わっていないことだけを保守的に検査します。質問topicとの意味関係、同義な言い換え、話者本人性は証明しません。target意味節が変わった場合は正しい言い換えでも完了creditを与えないことがありますが、その場合も再試験ではなく通常会話へ解放します。

## PDFのモード境界

標準モードでは、本人が明示選択したPDFを次の一ターンだけ扱います。これはPDFを完全にPII除去できたという意味ではなく、Cloud RunとVertex AIが原本の平文を扱います。

- 標準モードだけfile inputを有効にし、PDF・7 MiB以下を検証する
- 選択したファイルは次のvoice requestだけへserializeし、response後またはmode切替時に参照を解放する
- PDF本文、ファイル名、資料要約を会話状態やログへ残さない
- 厳格モードではfile read前に`document_privacy_blocked`で止める
- APIへ直接documentを付けた厳格requestもSTT・モデル推論前にfail-closedで拒否する

任意URL取得、PDFからの自動query、PDF原本の完全PII除去は実装していません。

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
- 標準モードの明示PDFは精密経路へ送り、厳格モードでは拒否する。医療、法律、金融、研究根拠は高リスク経路として扱い、精密経路が使えない時は実質回答を読み上げない
- STTが0より大きく0.65未満のconfidenceを返した場合、文字起こしをモデルへ渡さず、intentionalなら固定文で一度だけ聞き返し、foregroundと受動ambientは沈黙してマイクを閉じる。confidence 0はAPIが値を提供しなかった状態として扱い、低信頼判定とは区別する

`0.65`は未校正の補助境界であり、誤認識をゼロにする保証ではありません。Google Cloudが返す値を真の確率とはみなさず、実利用条件の音声でROC、聞き返し率、取りこぼし率を測って校正する必要があります。

LACの`Target Slot Coverage`、`Commitment Front Position`、`Meaning Preservation`は内部の制御・評価指標であり、モデルの自己申告だけを正解とはしません。現在は研究的な仮説であり、実際の会話データによる精度、誤介入率、校正の検証が必要です。

## 保証しないこと

- 端末内だけの処理
- E2EE
- 完全なPII除去
- 声紋による本人確認
- Passkey確認を、自然人の法的本人確認や現在の話者認証とみなせること
- 音声と検査・置換後の文字列がすべて日本リージョン内に留まること
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
- 未知field、不正MIME、過大音声を拒否する。厳格時のPDFはクライアントではfile read前、APIではSTT・モデル推論前に拒否する
- 測定されたSTT confidenceが低い時に文字起こしがモデルへ届かない
- 厳格時にlocal PII検査またはregional DLPが失敗したらraw transcriptがVertex AIへ届かず、応答検査が失敗したらTTS音声がwireへ届かず、同じ内容の言い直しも要求しない
- 状態tokenの改ざん、期限切れ、別UIDでの利用を拒否する
- 曖昧な潜在問いで断定的な再構成をしない
- 条件、不確実性、留保を変える再構成を拒否する
- draft自身のLACを偽装しても、独立監査と決定論的判定を迂回できない
- plannerとcriticが別のAを成功と申告しても、言い直し前のserver-only HMAC tagと不一致なら完了できない。tag自体が両model promptへ入らない
- AI自身の任意質問から、利用者が頼んでいない採点scopeを作らない
- 厳格requestにPDFを付けた場合はSTT・モデルを呼ばない。標準PDFまたは高リスク発話で精密経路が停止しても、高速draftの実質回答を返さない
- 自己修正中と介入価値が低い発話では沈黙する
- タブ非表示、pagehide、現在の仮上限である4分の無発話、30分の絶対上限、マイクtrack喪失でマイクを解放し、内容を含まない固定理由だけを通知してPausedへ移る。4分滞在を要求せず、利用者は一往復でも終了できる。Rust側は固定reason・version以外の通知を拒否し、通知を受けても停止処理を冪等に再実行してからPausedを表示する
- Pausedでは暗号化済みsession stateを消さず、明示的な再開操作だけがIntentionalとなる。Foreground再待受は既存のlive trackだけを再利用し、別マイクを自動取得しない
- 30秒の空captureと認証済みSTT no-speechはForegroundで再待受し、Intentional権限を継承しない。確定発話の送信失敗を自動再送しない
- 通常発話は1.2秒、1.6秒以上の発話は2.2秒、長い独話は5秒の間を待つ。クライアントのcaptureは最大3分30秒、live serverは最大4分または20 ms PCM 12,000 frame、live / HTTP turn全体は最大6分で必ず停止する
- 確定文字起こしが160 rune以上の時だけ、`extended speech`を現在turnの主点反射・構成に使う。分類も本文もcross-turn stateへ残さず、3分話せることを長期的な会話能力向上の証拠とは扱わない
- ログ、Firestore、Cloud Storageへ音声、逐語録、PDF本文が作られない

参考:

- [Cloud Speech-to-Text V2の対応モデルと地域](https://cloud.google.com/speech-to-text/docs/speech-to-text-supported-languages)
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
