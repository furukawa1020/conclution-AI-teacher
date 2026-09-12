# Mixed-Initiative Benchmark

Issue #223では、能動性をAIの発話数ではなく、本人の発話権と目的を守る判断として評価する。

## データを混ぜない

生成反例は有限状態機械の制御判断を壊す入力であり、人の会話を模倣したデータではない。
人手holdoutは別の配列、別の集計として扱う。
生成反例の成績を、人にとって自然または有用という証拠には使わない。

## 固定した生成契約

- schema: `kotae.mixed-initiative-counterexamples.v1`
- seed: `20260912`
- fixture数: `100000`
- SHA-256: `0d619f11dfa83ea584065a458ce0c421a6000884231d0bfb877f19b63037c0c2`

各fixtureは会話本文を持たない。
場面、発話floor、goal状態、provider fault、明示制御、介入回数、代理回答危険、別利用者state危険だけを有限値で持つ。
期待actionは同じ決定的oracleから再計算し、artifact内の値だけを書き換えても検証を通らない。

## 実システム結果の結合

3条件のrun artifactは、同じcorpus SHA-256と同じ40桁source commitを持たなければならない。
reactive baseline、always proactive、proposal controlledの順序、fixture数、fixture IDの順序も固定する。
欠落、重複、並べ替え、別commitの混在は比較前に失敗させる。

run artifactも会話本文を持たない。
実際に選んだ有限action、本人の自発発話・本人由来回答・目的完了・拒否・訂正・撤回の真偽値、有限の時間値だけを受け取る。
音声を出すactionに最初の有意味音声時刻がない場合、または音声を出さないactionに時刻がある場合は失敗させる。

各fixtureには`reactive_baseline`、`always_proactive`、`proposal_controlled`の3条件を実行する。
実行結果は生成fixtureそのものへ書き込まず、commitに束縛したbenchmark observationへ分離する。

## 百分位の境界

集計器は100標本未満のp50とp95を返さない。
人手で曖昧とされたholdoutは成功にも失敗にも含めず、曖昧件数と評価者一致度として残す。
結果が悪いvariantも削除しない。
