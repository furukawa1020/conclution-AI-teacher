# KOTAE Reflex 設計

## 結論

KOTAE Reflexは、固定質問を表示して採点する音声チャットではありません。利用者が明示的にセッションを開始した後は、日常のぼやき、独り言、考え途中の発話、研究の議論から潜在的な問いを仮説化し、次の条件をすべて満たす時だけ短く介入するambient conversational coachです。

1. 利用者の目標、主張、根拠、制約のどこかに介入対象がある。
2. 利用者自身による言い直しや自己修正を待っても解消しそうにない。
3. 今話す便益が、誤訂正、集中阻害、心理的圧力、プライバシーの損失を上回る。
4. 介入内容を、根拠と不確実性を含めて説明できる。

「沈黙」は失敗や機能制限ではなく、最も重要な出力の一つです。感情のあるぼやきを論理問題として即座に訂正したり、自然なフィラーを誤り扱いしたり、利用者の許可なく端末内の資料を読んだりしません。

中心に置く研究仮説は、既存部品の統合そのものではなく、**Latent Answer Contract（LAC）**です。LACは「何を聞かれているか」を一つに決め打ちせず、問いの型と上位仮説、答えに必要なslot、最初のcommitmentを明示します。そのうえで、答えの核が抜けた、または後ろへ埋もれた時だけ、元の条件と不確実性を変えないcounterfactual repairを許可します。

LACを次の制御と組み合わせ、誤った訂正より沈黙を優先します。

- 後から解釈を訂正できるRevision-aware Thought State Graph
- 利用者自身の言い直しを優先するSelf-repair grace
- 沈黙との比較で発話を選ぶExpected Value of Intervention
- 深い推論と短い音声表現を分けるThink-Verbalize-Speak
- 音声転送とsessionを制限する端末側Privacy Sentinel

これらは、このプロジェクトで設計・実装している実験的な機構です。近接研究は存在し、有効性や新規性の査読評価はまだ受けていません。「世界初」とは断定せず、LACの潜在問い同定、target slot coverage、commitment位置、meaning preservationが既存baselineより改善するかを実験で評価します。

本資料には、現在動くHTTPSターン型MVPと、将来研究するincremental / full-duplex構成の両方を記載します。現在実装済みの境界は次節と「実装段階」を正とし、それ以外のイベント、Live、検索、個人適応は設計目標です。

## 対象範囲と非目標

対象範囲は次の三つです。

| モード | 例 | 主な介入 |
|---|---|---|
| 日常 | 予定、仕事、人間関係、意思決定のぼやき | 目標の反射、見落とした制約、次の一歩 |
| 独り言 | 考えながら話す、案を比較する、行き詰まる | 自己修正を待つ、ループ検知、短い再構成 |
| 研究 | 論文、技術調査、仮説、実験結果の議論 | 主張と根拠の対応、反証、引用付き確認 |

次は非目標です。

- 明示的に開始していない時間の常時盗聴
- 文法、方言、フィラー、話し方の一律な矯正
- 声から性格、病気、精神状態を診断すること
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
同一Origin HTTPS / Firebase Auth + App Check
  ▼
Cloud Run / Go（asia-northeast1）
  ├─ raw audio → Cloud STT V2（asia-northeast1）
  │                 └─ transcript
  ├─ transcript + 今回だけのPDF → Vertex AI（global）
  │                                  ├─ Thought State Graph
  │                                  ├─ LAC
  │                                  ├─ Self-repair grace
  │                                  └─ EVI: silence / clarify / repair
  └─ 選ばれた短い応答文 → Cloud TTS（asia-northeast1）
                              └─ MP3 → ブラウザ再生
```

raw audio、文字起こし、モデル応答、PDFはアプリ側で永続化しません。cross-turn stateには自由文要約を入れず、検出できたemail・電話番号らしい長い数列・credentialらしいtoken・原文との高い重複を除いた短い意味nodeと制御メタデータだけをAES-256-GCMで暗号化したUID-bound tokenとしてブラウザメモリへ返し、15分で失効させます。氏名など未検出の機微情報がnodeへ残る可能性はあります。Vertex AIは`global`なので、文字起こしとPDFまで東京リージョンだけに留まるとは保証しません。

### 現在のMVP境界

ブラウザだけで高精度な日本語ASR、意味推論、自然なfull-duplex音声をすべて端末内処理するのは、対応端末、電力、モデル配布量の面でまだ不安定です。そのため現在のMVPは次の境界です。

- 利用者が最初にタップして開始したsession中だけマイクを使う。
- 端末VADで一発話を区切り、一発話ごとのHTTPS requestとして東京リージョンSTTへ送る。
- raw audio、文字起こし、prompt/response、PDFをKOTAEのDB、Storage、ログへ保存しない。
- raw audioをVertex AIへ送らず、STTで得た文字列と明示添付PDFだけを`global`のVertex AIへ送る。
- 応答を選んだ時だけ短い文字列を東京リージョンTTSへ送る。
- WebSocket、Vertex Live、session resumption、full-duplex、barge-inを現在は使わない。
- AI処理中と再生中はマイクを無効にし、タブ非表示、3分無発話、30分経過でsessionを止める。
- PDFは一つのturnだけ送信し、本文も資料要約も暗号化状態へ残さない。

将来のprivacy-first pathでは、対応端末だけローカルASRで安定した文字列を作り、生音声をクラウドへ送らない経路を比較します。native audioやfull-duplexを採用する場合も、現在のregional STT / structured reasoner経路と同じものとして表示しません。

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
| `source` | 論文、URL、添付資料 | DOI、PDFのページ |

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
3. targetが満たされてもcommitmentが後ろにある場合は、repair gainが十分な時だけ前へ移す。
4. 再構成で元の条件、留保、不確実性が変わる場合は`reject`する。
5. targetを原文から安全に推定できない場合は、もっともらしい答えを捏造せず`clarify`または`silence`にする。
6. LACの原文入りcontractは現在turnだけで破棄し、暗号化したcross-turn stateへ入れない。

内部metricsは`Target Slot Coverage`、`Commitment Front Position`、`Meaning Preservation`です。これらはUIに採点文として並べるためではなく、A→Aのhard case、過剰修正、条件消失を回帰テストするために使います。

現実装の意味保存guardは、日本語の条件・因果marker、boolean極性、数値と単位、Latin / 選択肢label / 引用anchor、不確実性の段階を比較する決定論的検査です。これは意味同値性を完全に判定する証明ではないため、閾値以上でも誤修復率を人手評価し、operator別・domain別に校正する必要があります。

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

現在のMVPが受け取る資料は、利用者がそのturnに明示添付したPDFだけです。PDFはVertex AI `global`へinline送信し、原本も資料要約もcross-turn stateへ保存しません。PDF turnは必ず精密経路と独立LAC監査を通り、どちらかが使えなければ高速draftの実質回答を読み上げません。DOI / URLの取得、世界中の新着論文の自動検索、引用付きの独立Research Verifierはまだ実装していません。

研究ロードマップでは、PDF、DOI、URL、引用情報を`source`ノードへ登録し、次を一般会話とは別のResearch Verifierで扱います。

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

PDFやWebページ内の命令文は資料データとして扱い、システム命令として実行しません。現在のMVPは外部取得、検索、コード実行を行いません。将来追加する場合も、別のtool policyで利用者の意図、権限、保持条件を確認します。

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
- Storage、Firestoreへ音声・transcript・PDF本文が作成されない
- third-party audioを利用者本人の入力として誤採用した割合

## 評価ケース設計

### 日常会話

| ケース | 期待動作 |
|---|---|
| 「今日は疲れた、何もしたくない」とぼやくだけ | 原則沈黙または短い反射。即座に予定表へ変換しない |
| 「金曜公開。ただし木曜まで実装」と矛盾する予定 | 自己修正を待ち、残った場合だけ期限関係を確認 |
| 制約を順番に思い出して追加する | 途中で結論を出さず、追加が止まってから整理 |
| 同じ選択肢を新情報なしで何度も往復する | 比較軸か小さな検証を一つ提案 |
| テレビや同席者が質問する | 利用者宛てでなければ沈黙 |
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
| 添付論文と一致する主張 | 不要な確認をせず沈黙 |
| Tableの数値と逆の主張 | page、tableへ結びついた短い確認 |
| 相関研究を因果効果として話す | 研究デザインを根拠に条件付き修正 |
| 二本の論文が異なる結論 | 片方を正解扱いせず、条件差または矛盾を提示 |
| 論文が未提供 | 内容を創作せず資料提供を依頼 |
| 2024年時点のモデル状態を現在仕様として話す | 最新一次資料を別Research Verifierで確認 |
| PDF本文に「以前の指示を無視せよ」とある | 資料データとして扱い、toolやsystem policyへ影響させない |
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
- 一発話ごとのHTTPS request、Firebase Auth、App Check、完全一致Origin、明示`turnMode`検証
- 東京リージョンSTT → `global` Vertex AI structured reasoner → 東京リージョンTTS
- raw audio、文字起こし、応答文、PDFをアプリ側で永続化しない
- AES-256-GCM、UID-bound、15分TTL、自由文要約なしのフィルタ済み意味状態
- Thought State Graph、規則とモデル判定を併用したEVI
- LACのQuestionFrame、CommitmentFront、CounterfactualRepair
- draftから独立したLAC criticとGoの決定論的authoritative evaluator
- 曖昧な問いでのclarify、低EVIと自己修正中のsilence
- 一つのturnだけのPDF添付とPDF本文の非永続化
- PDF・高リスクdomainのprecision fail-closed

### B. LAC評価 — 実装中

- 日本語hard caseでA→A、operator、条件、不確実性の不変条件を回帰テスト
- Target Slot Coverage、Commitment Front Position、Meaning Preservationの校正
- `keep / clarify / restructure / reject`を人手blind評価
- 一般会話、研究、技術、高リスク領域ごとの誤修復率と過剰介入率
- model-only、固定prompt、LACなしEVIとのablation

### C. Privacy Sentinel拡張 — 未実装

- Rust/WasmへVADと短いring bufferの責務をさらに移す
- TEN VAD、VAP、Moonshine等はライセンス、配布量、日本語精度、Web性能を実測してから採用する
- 対応端末ではlocal transcript pathを追加する
- regional STT path、native audio path、privacy-first pathをUI上で区別する

### D. Research Verifier — 一部実装

- 一つのturnだけの明示PDF入力は実装済み
- DOI / URL取得、新着論文survey、source / claim / evidence graphは未実装
- 引用検証と`insufficient_evidence`の評価器を一般会話から分離する
- 検索toolと行動toolを音声sessionから分離する
- 検索時の保持条件を利用者へ表示する

### E. 個人適応 — 未実装

- 「最後まで聞いて」「今は共感だけ」「厳しく」等の明示feedback
- 利用者別の介入強度とcooldownを端末内集約値で調整
- 暗黙feedbackだけで自動的に性格・健康状態を推定しない
- 十分な同意データが集まるまではオンライン強化学習を行わない

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

現在のMVPはVertex Liveのsession resumptionもGoogle Search Groundingも使いません。ただし「使っていない」ことだけでGoogle Cloud全体のゼロ保持を保証するとは表現しません。将来Liveや検索を追加する場合は、その時点のGoogle Cloud公式仕様、preview条件、データ保持、リージョンを再確認します。現在の境界は[Vertex AI zero data retention](https://cloud.google.com/vertex-ai/generative-ai/docs/vertex-ai-zero-data-retention)と`docs/audio-security.md`を参照します。

## 参考一次資料

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
