# 「Aと聞かれてAと答えられない」と実装の対応

## 結論

PDFが扱う中心課題は、AIが質問へ正答できないことではない。相手から質問された本人が、理解していても作業記憶や不安などの負荷によって、求められた答えを先に取り出せないことである。

そのためKOTAEは、次の二つを別の経路として扱う。

1. `assistant`: 利用者がKOTAEへ質問し、KOTAEが答える。
2. `respondent`: 別の人から利用者へ向けられた質問と、利用者自身の回答試行を受け取り、本人の意味を変えずに組み直す。

統合だけを新規性とはしない。検証対象は、質問が要求するslot、本人が実際に発話したevidence、最初のcommitment、意味保存を、実行時契約として判定できるかである。

## PDFの場面との対応

| PDFの場面 | KOTAEの動作 |
|---|---|
| 「論文のこの言葉ってどういうこと？」 | `definition` operatorと`definition` slotを要求する |
| 「結局このプロジェクトで何をやりたいの？」 | `purpose` operatorと`purpose` slotを要求する |
| 質問だけ分かり、本人の答えがまだない | 結論を代作せず、「まとまっていなくてよいので今の答えを話して」と一問だけ返す |
| 本人の答えが理由や前置きの後ろにある | 本人の回答中に完全一致するtarget evidenceを先頭へ移す |
| 条件、否定、不確実性、数値がある | 追加・削除・強さの変更を拒否する |
| 本人が自分で言い直しかけている | self-correction grace中は原則として沈黙する |
| 「努力」「練習不足」と責められてきた | 訓練、矯正、普通らしさを要求する文を生成方針から除外する |

## 意味を変えないための境界

`respondent`経路では、モデルの自然さだけを信用しない。

```text
相手の質問
  └─ operator / subject / required slots

本人の回答試行
  └─ slotごとの完全一致evidence / protected spans

再構成案
  └─ 元回答にある意味節の順序変更だけ
       ├─ target commitmentが先頭か
       ├─ 必須slotを満たすか
       ├─ 否定・条件・数値・不確実性を保つか
       └─ 新しい固有内容を足していないか
```

一つでも安全に確定できなければ、代わりの答えを創作せず確認へ戻る。保留状態には質問operator、短いsubject、required slotsだけを残し、逐語録、本人の回答試行、再構成案、evidence本文は残さない。

## Research Verifierの現在地

現在の公開経路に接続するResearch機能は、検証器の安全な入口である。

- intentional turn全体が「Crossrefで DOI … を調べて」に完全一致したDOI照会
- intentional turn全体が「外部検索で『テーマ』の最新論文を探して」に完全一致した論文検索
- 固定された`https://api.crossref.org`だけへのread-only request
- redirect拒否、timeout、response上限、queryのPII・credentialらしい値とpercent encodingの検査
- 発話中の一意なDOI、モデルが選んだDOI、Crossrefが返したDOIの完全一致
- DOI、日付、URLの正規化と重複排除
- 結果を必ず`discovery_metadata_not_claim_evidence`、`needs_primary_evidence`として扱う
- abstractを音声応答や画面へ再配布せず、title、DOI、公開日、一次資料へのlinkだけを現在のturnへ返す

topic探索で使うのはCrossrefのindex date filterであり、発表日の新しい順ではない。「Crossrefの索引日が指定期間内の書誌候補」として扱う。これは世界中のWebを巡回して本文を読んだ状態でも、主張の真偽を検証した状態でもない。次の段階では、利用条件を確認した複数の一次source、取得権限のある本文、claim-evidence alignment、撤回・更新情報、矛盾する結果、定期収集の同意と保持方針を別々に実装・評価する。

## 現在も解決していないこと

- 話者本人認証と、同席者・テレビ・合成音声の識別
- E2EEと完全なPII除去
- 55秒を超える実人声の長い独話を、途中確定せず増分校正する経路
- 任意Web、複数論文本文、claim単位の自動検証
- 本人を対象にした有効性、誤修復率、負荷軽減の実証

これらを「実装済み」「安全」とは表示しない。現在の受け答え支援では、利用者が相手の質問を自分で言い直し、その後に自分の答えを話す形だけを対象にする。

## 合否指標

PDFへの適合は、見栄えやAPI数ではなく次で測る。

- `Target Slot Coverage`: 質問が要求したslotを先に満たせた割合
- `Commitment Front Position`: 最初の実質回答が理由や前置きより前にある割合
- `Meaning Preservation`: 人手評価で否定、条件、数値、不確実性、固有内容が不変だった割合
- `Fabrication Rate`: 本人の原回答にない内容を一つでも足した割合
- `Unnecessary Intervention Rate`: 自己修正できた場面へ割り込んだ割合
- `First-person Helpfulness`: 本人が「代わりに決められた」のではなく「自分の答えを出せた」と評価した割合
