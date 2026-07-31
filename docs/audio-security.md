# 音声セキュリティ設計

## 現在の保証範囲

現在の公開音声経路は「録音を暗号化して長期保存するサービス」ではありません。利用者が明示的に開始したセッション中に、一つの発話を認識、推論、必要なら音声合成し、アプリ側では音声、文字起こし、モデル応答、PDFを永続化しない構成です。

ここでいう「アプリ側で保存しない」は、KOTAEのFirestore、Cloud Storage、アプリログ、ブラウザのlocalStorageへ会話データを書かないという意味です。音声は発話ごとのrequest dataとしてregional STTへ渡し、KOTAEはrequest終了後に履歴を保持しません。一方、処理に必要な平文は端末、Cloud Run、Google Cloudの各APIから見えます。E2EE、完全な端末内処理、メモリフォレンジックに対する消去保証、Google Cloud全体のゼロ保持を意味しません。管理サービス側のデータ利用・ログ条件は公式契約とproject設定を別に確認します。

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
  ├─ transcript + 今回だけのPDF ──→ Vertex AI（global）
  │                                    ├─ KOTAE Reflex / LAC
  │                                    └─ silence または短い応答文
  ├─ 明示したDOI / 新着topic ──→ Crossref REST API
  │                                  └─ 書誌候補（claim evidenceではない）
  └─ 応答文 ──→ Cloud Text-to-Speech（asia-northeast1）
                    └─ MP3 ──→ ブラウザ再生
```

| データ | 処理先 | アプリ側の永続化 | セッション継続に残るもの |
|---|---|---|---|
| マイク音声 | ブラウザ、Cloud Run、東京リージョンSTT | なし | なし |
| 文字起こし | Cloud Run、Vertex AI `global` | なし | 自由文要約は残さない。検出できたemail・電話番号らしい長い数列・credential token・原文との高いn-gram重複を除いた短いgraph nodeだけが入り得る |
| モデル応答文 | Cloud Run、東京リージョンTTS | なし | 原文は状態へ保存しない |
| 合成音声 | Cloud Run、ブラウザ | なし | 再生後は参照を解放する |
| PDF | Cloud Run、Vertex AI `global` | なし | 本文も資料要約もcross-turn stateへ残さない |
| 明示した研究query | Cloud Run、Crossref | なし | DOI、topic、候補をcross-turn stateへ残さない |
| 研究候補 | Cloud Run、ブラウザ | なし | title、DOI、日付、sourceを現在のresponseだけへ返す |
| 会話状態 | ブラウザメモリ、次ターンのCloud Run | サーバーDBには保存しない | フィルタ済み意味グラフと制御メタデータ、15分TTL |
| 音声レート制限 | Firestore | 48時間TTL | UIDまたはFirebase App IDのSHA-256由来document IDと回数・時刻 |

STTとTTSは`asia-northeast1`のリージョナルAPIエンドポイントへ固定しています。一方、意味推論に使うVertex AIのロケーションは`global`です。したがって、raw audioはSTT / TTS境界では東京リージョンで処理されますが、文字起こし、応答文、添付PDFまで日本国内に限定されるとは保証しません。

STTは`asia-northeast1`・`ja-JP`の`long`だけを使います。自然な会話の途中の短い間を文末と誤認しにくい会話向けlong-form modelを選び、端末側VADとの一致をcommit条件にしてproviderの判定だけで発話を確定しません。STTのIAM拒否、model利用不可、timeout、decode失敗はすべてfail-closedにし、別modelや東京域外へ自動退避しません。

## マイクとセッション制御

- 最初のタップを明示的な開始操作とし、開始前はマイクを取得しない
- 各requestは`turnMode: intentional | foreground | ambient`を必須とし、状態tokenの有無から権限を推測しない。foregroundは返答を期待するが、外部作用や状態更新についてはambientと同じ制限を保つ
- 端末側VADは発話区間を決めるためだけに使い、声紋認証、感情診断、病気や性格の推定に使わない
- AI処理中と合成音声の再生中も、利用者が開始した会話セッション内では訂正・割り込みを受けるためマイクトラックを有効にする。端末内VADが確認する前の音声は送信せず、確認した割り込みだけをForeground turnとして送る
- 確認前PCMはAudioWorklet内だけに保持し、通常発話は最大25 frame、割り込みは最大20 frameに固定する。VAD確認後はAudioContextのsample-clock cutoff、session generation、連続sequenceを検証し、credit制御でMessagePortの未処理数も固定する。turn確定は全PCMの`sealed`確認後だけ許可する
- 割り込み待機を含むセッション全体を3分の無発話または30分の絶対上限で終了し、期限時は通信、PCMリング、録音、再生、マイクトラックを同じepochで破棄する
- タブが非表示になった時と`pagehide`時に録音と再生を止め、マイクトラックを解放する
- 無発話が3分続いた時、または開始から30分経過した時にセッションを終了する
- 一発話は音声ありで最大55秒、無音で最大30秒とし、音声は2 MiB、PDFは7 MiB、Base64・状態token・JSONを含むrequest envelopeは13 MiBを上限にする
- 会話状態とPDFはJavaScript変数にだけ保持し、localStorageへ保存しない。Firebaseの匿名認証だけは`browserSessionPersistence`を使う

JavaScriptのガベージコレクションや文字列の複製は完全には制御できません。クライアントは使用後に参照を解放し、Go側は受信byte sliceを可能な範囲でclearしますが、これを暗号学的なRAM消去保証とは表現しません。

現在のVADは発話区間だけを見ており、話者本人認証ではありません。同席者、テレビ、合成音声を利用者本人だと安全に識別する機能もありません。そのため公開UIは、周囲の質問を常時取り込む使い方ではなく、利用者自身が「こう聞かれた」と質問を言い直してから回答を話す使い方に限定して案内します。

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

- Firebase ID token
- Firebase App Check token
- 許可済みFirebase App ID
- 完全一致する`Origin: https://kotae-ai.web.app`。missing、`null`、別origin、重複Originを拒否
- `application/json`と許可済み音声MIME
- サイズ上限、strict Base64、未知JSON fieldの拒否
- JSON本文とBase64を読む前に消費するUID単位とFirebase App単位の二段階レート制限
- request timeout

レスポンスの`caption`は、実際にTTSへ渡した最終`SpokenReply`だけです。文字起こし、内部推論、LAC本文は返しません。意図的な沈黙では音声を空、`caption`を`null`にします。

PDFは`application/pdf`、サイズ上限、`%PDF-` magicを確認します。PDF内の文章は命令ではなく信頼できない資料としてモデルへ渡し、外部ツール実行や権限変更に使いません。

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

tokenにはフィルタ済みでも会話由来の意味nodeが含まれ得るため、秘密でないデータとは扱いません。また、Cloud RunはSecret Managerの鍵を使って復号できるためE2EEではありません。秘密へのアクセスはCloud Runの実行サービスIDだけに限定します。

## PDFの「今回だけ」

PDFは利用者が選択した後の一つの音声ターンにだけ添付します。request完了時にブラウザ側の参照を外し、Cloud Run側のbyte bufferをclearし、FirestoreやCloud Storageへ原本を保存しません。

ただし、次を明示します。

- PDF本文は推論のためVertex AI `global`へ送られる
- PDF turnは高速draftのdomain判定に依存せず必ず精密経路と独立LAC監査を通す
- 音声turnの精密推論と独立LAC監査は、それぞれ3.5秒のone-shotとする。高リスクでは精密modelと`HIGH` thinkingを維持するが、同期criticの再試行や別modelへの回復は行わず、期限内に完了しなければ固定確認へfail-closedする
- 精密経路または独立監査が使えない場合、実質回答を高速draftへfallbackせず、intentionalとforegroundなら短い固定確認、受動ambientなら沈黙にする
- 資料本文と資料要約は暗号化状態tokenへ残さない
- JavaScript、Go runtime、管理されたGoogle Cloudサービス内部の一時コピーまで物理消去を証明するものではない

## ログと永続データ

音声、文字起こし、モデルprompt/response、PDF、Firebase token、App Check token、状態token、秘密鍵をアプリログへ出しません。音声APIの運用ログは次に限定します。

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
- PDF、医療、法律、金融、研究根拠は高リスク経路として扱い、精密経路が使えない時は実質回答を読み上げない
- STTが0より大きく0.65未満のconfidenceを返した場合、文字起こしをモデルへ渡さず、intentionalなら固定文で一度だけ聞き返し、foregroundと受動ambientは沈黙してマイクを閉じる。confidence 0はAPIが値を提供しなかった状態として扱い、低信頼判定とは区別する

`0.65`は未校正の補助境界であり、誤認識をゼロにする保証ではありません。Google Cloudが返す値を真の確率とはみなさず、実利用条件の音声でROC、聞き返し率、取りこぼし率を測って校正する必要があります。

LACの`Target Slot Coverage`、`Commitment Front Position`、`Meaning Preservation`は内部の制御・評価指標であり、モデルの自己申告だけを正解とはしません。現在は研究的な仮説であり、実際の会話データによる精度、誤介入率、校正の検証が必要です。

## 保証しないこと

- 端末内だけの処理
- E2EE
- 声紋による本人確認
- 音声・文字・PDFがすべて日本リージョン内に留まること
- 第三者クラウドを含む絶対的なゼロデータ保持
- ブラウザ拡張、OSマルウェア、画面・スピーカーの盗み見への防御
- モデル回答の正しさ、最新情報の自動保証
- 保存音声の履歴、再生、共有、後日再評価

`crates/audio_vault`は将来の同意制履歴を検討するための暗号化コアで、現在の公開音声経路には接続していません。履歴機能を追加する場合は、raw audio保存、応答音声保存、後日再評価、共有、品質改善を別々に同意させ、保存先、削除、鍵管理、監査を改めて設計します。

## 最低限の検証

- ID token、App Check、Originのどれかが不正ならモデルを呼ばない
- missing / unknown `turnMode`を拒否し、intentional・foreground・ambientを状態tokenから推測しない
- 不正本文でも認証後はUID枠とApp枠が先に消費される
- 未知field、不正MIME、過大音声、過大PDF、PDF magic不正を拒否する
- 測定されたSTT confidenceが低い時に文字起こしがモデルへ届かない
- 状態tokenの改ざん、期限切れ、別UIDでの利用を拒否する
- 曖昧な潜在問いで断定的な再構成をしない
- 条件、不確実性、留保を変える再構成を拒否する
- draft自身のLACを偽装しても、独立監査と決定論的判定を迂回できない
- PDF・高リスク発話で精密経路が停止しても、高速draftの実質回答を返さない
- 自己修正中と介入価値が低い発話では沈黙する
- タブ非表示、session停止、error時にマイクを解放する
- ログ、Firestore、Cloud Storageへ音声、逐語録、PDF本文が作られない

参考:

- [Cloud Speech-to-Text V2の対応モデルと地域](https://cloud.google.com/speech-to-text/docs/speech-to-text-supported-languages)
- [Speech-to-Text data usage FAQ](https://cloud.google.com/speech-to-text/docs/data-usage-faq)
- [Cloud Text-to-Speech Chirp 3 HD](https://cloud.google.com/text-to-speech/docs/chirp3-hd)
- [Text-to-Speech regional endpoints](https://cloud.google.com/text-to-speech/docs/endpoints)
- [Text-to-Speech data logging](https://cloud.google.com/text-to-speech/docs/data-logging)
- [Vertex AI zero data retention](https://cloud.google.com/vertex-ai/generative-ai/docs/vertex-ai-zero-data-retention)
- [Firebase App Check custom backend](https://firebase.google.com/docs/app-check/custom-resource-backend)
- [Secret Manager access control](https://cloud.google.com/secret-manager/docs/access-control)
- [Cloud Run service identity](https://cloud.google.com/run/docs/securing/service-identity)
