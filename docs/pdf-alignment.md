# 「Aと聞かれてAと答えられない」と実装の対応

## 結論

PDFが扱う中心課題は、AIが質問へ正答できないことではない。相手から質問された本人が、理解していても作業記憶や不安などの負荷によって、求められた答えを先に取り出せないことである。

そのためKOTAEは、次の二つを別の経路として扱う。

1. `assistant`: 利用者がKOTAEへ質問し、KOTAEが答える。
2. `respondent`: 別の人から利用者へ向けられた質問と、利用者自身の回答試行を受け取り、答えの核を本人自身が先に言い直せるよう支える。

統合だけを新規性とはしない。検証対象は、質問が要求するslot、本人が実際に発話したevidence、最初のcommitment、意味保存を、実行時契約として判定できるかである。

この支援は会話を訓練や課題として感じさせることを目的にしない。日常会話を優先し、本人が明示的に助けを求めた時だけ一問を短く返す。一往復で閉じてよく、滞在時間や反復回数を達成条件にしない。

## 公開音声経路での回答支援切替

標準のPDFなしlive turnは通常、`us-central1`のVertex AI Native Audioへ原音を送る。最終入力captionから、本人自身が相手から聞かれた質問への回答支援を明示的に頼んだとサーバーが判定した場合は、Nativeの応答音声を一frameも利用者へ解放せず、review済みのzero-output fallbackだけを許可する。その場合、ブラウザが保持している同じ発話のcaptureを、東京リージョンSTT、`global`の文字列推論、LAC、Respondent Coach、東京リージョンTTSの段階経路へ再送することがある。したがって初回切替では、同じ原音がNative Audioの入力処理後にSTTでも処理され得る。これは厳格モードではなく、regional DLPを通過してからNative Audioへ送る経路でもない。

Respondent Coachが一問を保留している間は、サーバーが認証した有限の保留stateを権限の正本とし、クライアントもそのphase/actionを明示的なbooleanへ写してNative Audioを選ばない。`complete`と、聞き直しを止める`release`は保留状態ではないため、その表示後に開始する次turnから通常のNative Audioへ戻る。初回切替には再送と段階処理があるため、通常Nativeより遅くなり得て、1秒での発話開始は保証しない。

UIの`complete`は「聞かれたことへの答えが届きました」という今回限りのreceiptに留める。これは当該turnで質問に対応するevidenceを受け取ったという状態表示であり、「Aを先に言えた」「上達した」「別の人にも同じように答えられる」と判定・実証する表示ではない。

約3分まとまらずに話した場合も、時間だけで失敗や能力不足と判定しない。現在のlive経路は端末で録音開始から最大3分30秒、Cloud Run側で最大4分のcaptureを受け、Go側のlive接続を6分、Cloud Runのrequest timeoutを420秒に制限する。発話が確定しない無音候補は録音開始後最大30秒で終了するため、その上限直前から話し始めても約3分は残るが、3分30秒の実発話を保証するものではない。commit後のfinal transcriptが160 Unicode code point以上の場合だけ、サーバーが今回限りの長い発話と判定し、現在turn内で本人が明示した中心点を、条件や不確実性を変えず第一文へ置いて自然に応答する。160未満でも通常会話は続け、長さだけを根拠に指導、採点、言い直し要求をしない。

## PDFの場面との対応

| PDFの場面 | KOTAEの動作 |
|---|---|
| 「論文のこの言葉ってどういうこと？」 | `definition` operatorと`definition` slotを要求する |
| 「結局このプロジェクトで何をやりたいの？」 | `purpose` operatorと`purpose` slotを要求する |
| 質問だけ分かり、本人の答えがまだない | 結論を代作せず、「まとまっていなくてよいので今の答えを話して」と一問だけ返す |
| 本人の答えが理由や前置きの後ろにある | AIが代読せず、本人へ「今の答えを先に、もう一度」と一問だけ返す |
| 本人が答えを先に言えた | 通常は支援を閉じる。同じ明示支援中に本人が厳密句「理由まで一問お願いします」と頼んだ時だけ、理由・根拠・最初の一歩のうち質問の型に合う一問を尋ねる |
| 条件、否定、不確実性、数値がある | 追加・削除・強さの変更を拒否する |
| 本人が自分で言い直しかけている | self-correction grace中は原則として沈黙する |
| 「努力」「練習不足」と責められてきた | 努力不足と決めつける文や普通らしさを要求する一律矯正を除外する。本人が選んだ回答練習も採点せず、聞き直しは一度だけにする |

## 意味を変えないための境界

`respondent`経路では、モデルの自然さだけを信用しない。

```text
相手の質問
  └─ operator / subject / required slots

本人の回答試行
  └─ slotごとの完全一致evidence / protected spans

内部の再構成候補（読み上げない）
  └─ 元回答にある意味節の順序変更だけを検証材料にする
       ├─ target commitmentが先頭か
       ├─ 必須slotを満たすか
       ├─ 否定・条件・数値・不確実性を保つか
       └─ 新しい固有内容を足していないか
```

一つでも安全に確定できなければ、代わりの答えを創作せず確認へ戻る。AIの再構成候補は本人の答えとして読み上げない。本人がAを先に言えたら通常は支援を閉じる。同じ明示支援中に本人が厳密句「理由まで一問お願いします」と頼み、そのAを独立検証できた時だけ一段広げる。この任意の一問は最初の実質的な返答で完了し、成否をもう一度試験しない。聞き直しは一度だけで、次に成立しなければ指導を止めて通常会話へ解放する。

## Research Verifierの現在地

現在の公開経路に接続するResearch機能は、検証器の安全な入口である。

- intentional turn全体が「Crossrefで DOI … を調べて」に完全一致したDOI照会
- intentional turn全体が「外部検索で、テーマは何々の最新論文を探して」に完全一致した論文検索
- 固定された`https://api.crossref.org`だけへのread-only request
- redirect拒否、timeout、response上限、queryのPII・credentialらしい値の検査
- NFKC差・Unicode format文字・ASCII外のDOI suffixのfail-closed拒否と、HTML entity・percent encoding・PIIらしいBase64・Base32文字列の復号再検査。分割Base64も結合し、復号値を元の文へ戻して検査する
- topic文字をUnicodeの文字・結合記号、ASCII数字、空白、hyphen、数字間のdecimal pointへ限定し、任意URLや命令をqueryへ混ぜない
- topic内の節区切り・取消語、DOIへ付加されたcomma・semicolon・取消語の拒否
- 発話中の一意なDOI、モデルが選んだDOI、Crossrefが返したDOIの完全一致
- DOI、日付、URLの正規化と重複排除
- 結果を必ず`discovery_metadata_not_claim_evidence`、`needs_primary_evidence`として扱う
- abstractを音声応答や画面へ再配布せず、title、DOI、公開日、一次資料へのlinkだけを現在のturnへ返す

topic探索で使うのはCrossrefのindex date filterであり、発表日の新しい順ではない。「Crossrefの索引日が指定期間内の書誌候補」として扱う。これは世界中のWebを巡回して本文を読んだ状態でも、主張の真偽を検証した状態でもない。次の段階では、利用条件を確認した複数の一次source、取得権限のある本文、claim-evidence alignment、撤回・更新情報、矛盾する結果、定期収集の同意と保持方針を別々に実装・評価する。

## セキュリティ機能の現在地

- PasskeyのWebAuthn ceremonyを実装し、仮名Firebase accountの操作をuser verification付き署名で確認する。秘密鍵はPasskey providerが管理し、KOTAEのブラウザコードとサーバーは受け取らない（同期や保管の方式はproviderに依存する）。これは法的な本人確認や現在の話者認証ではない
- 音声はSpeech-to-Textで平文処理される。厳格モードでは、文字起こしと応答文をCloud Run内の決定論的検査とregional DLPの両方が`clear`とした場合だけ後段へ進め、検出・timeout・権限エラー・mode不一致をfail-closedにする。標準モードに同じ保証があるとは表示しない
- 標準モードで回答支援を明示した初回turnは、`us-central1`のNative Audioが原音を入力処理した後、Native応答を解放せず、同じcaptureを`asia-northeast1`のSTTへ再送することがある。保留中の後続turnは段階経路を維持し、`complete` / `release`後の次turnからNativeへ戻る
- DLPにも検出漏れがあり得るため、完全なPII除去とは呼ばない。Cloud Run、Speech-to-Text、DLPが平文を扱うためE2EEとも呼ばない
- KOTAEのFirestore、Cloud Storage、アプリログへ原音・文字起こし・モデル本文を保存しない。第三者クラウド全体の絶対的なゼロ保持は保証しない
- 標準モードのPDF添付は利用者が選んだ次の一ターンだけCloud RunとVertex AIへ渡し、応答後に参照を解放する。厳格モードではfile read前とAPIのSTT・推論前の両方で停止する
- 約3分の独話は、端末の録音開始から最大3分30秒、Cloud Run受信4分、Go live deadline 6分、Cloud Run request timeout 420秒の順に上限を分離して受ける。発話が確定しない無音候補は最大30秒で、その上限直前から話し始めても約3分を確保するが、3分30秒の実発話は保証しない。Cloud Speech-to-Textの5分上限まで使い切らず、provider境界まで60秒を残す
- 圧縮音声のHTTPS fallbackは2 MiB上限で、約3分を通せる保証はない。上限を超えた場合は保持中のchunkを全て破棄し、先頭だけのpartial audioをuploadしない。live PCMが生きていればfallback超過だけでlive captureを止めない
- live WebSocketは音声受信前にFirestoreの短命leaseを取得し、同じ仮名Firebase UIDの同時接続を1本へ制限する。接続終了時にpipelineの停止を確認できない場合は即時解放せず7分TTLまでleaseを保持する
- final transcriptが160 Unicode code point以上の場合だけ、クライアントから指定できないサーバー由来の印を付ける。中心点の根拠は現在turnのfinal transcriptだけとし、否定、条件、数値、不確実性を保つ。途中候補や過去turnから補わず、安全に一つへ定められない時は創作せず確認へ戻る。この印と逐語録・発話本文はcross-turn stateへ入れないが、長い発話も通常会話と同じ状態更新の対象であり、検査・フィルタ後の抽象化済み・有限化されたgoal、claim、open loopなどは暗号化された15分stateへ残り得る
- opt-in、固定測定窓、未見質問への有限回答、端末内保存、時点別の生観測表示、撤回・全削除を備えた個人内長期測定は実装した。有限回答、1〜5、日単位の測定日、無作為な端末内ID、同意・schema versionだけを扱い、音声、文字起こし、自由文、Firebase UID、時刻は保存しない。別tab競合はgeneration fenceで停止し、全削除後は個人情報を含まない固定markerだけを残す。ただし署名付き研究台帳や比較試験ではなく、長期効果は未実証である。測定用コードを書くことと、本人を対象にした有効性を示すことを同一視しない

## 現在も解決していないこと

- 話者本人認証と、同席者・テレビ・合成音声の識別
- E2EEと完全なPII除去（現在のDLP境界は低減策であり解決ではない）
- 録音開始から3分30秒を超える一turn capture、Cloud Speech-to-Textのstreaming上限をまたぐsession継続、発話途中の意味へ介入する完全増分校正。端末上限には発話前の無音や発話中の間も含まれる
- 任意Web、複数論文本文、claim単位の自動検証
- 本人を対象にした有効性、誤修復率、負荷軽減の実証
- 回答支援の初回zero-output切替を含む、実端末での発話終了から応答開始までのp50 / p95

これらを「実装済み」「安全」とは表示しない。現在の受け答え支援では、利用者が相手の質問を自分で言い直し、その後に自分の答えを話す形だけを対象にする。長期効果は、事前登録した比較試験、未見質問、一定期間後の追跡、本人の同意と撤回・削除を含む評価が終わるまで主張しない。

## 合否指標

PDFへの適合は、見栄えやAPI数ではなく次で測る。

- `Target Slot Coverage`: 質問が要求したslotを先に満たせた割合
- `Commitment Front Position`: 最初の実質回答が理由や前置きより前にある割合
- `Meaning Preservation`: 人手評価で否定、条件、数値、不確実性、固有内容が不変だった割合
- `Fabrication Rate`: 本人の原回答にない内容を一つでも足した割合
- `Unnecessary Intervention Rate`: 自己修正できた場面へ割り込んだ割合
- `First-person Helpfulness`: 本人が「代わりに決められた」のではなく「自分の答えを出せた」と評価した割合
