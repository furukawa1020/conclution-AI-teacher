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

各fixtureには`reactive_baseline`、`always_proactive`、`proposal_controlled`の3条件を実行する。
実行結果は生成fixtureそのものへ書き込まず、commitに束縛したbenchmark observationへ分離する。

## 百分位の境界

集計器は100標本未満のp50とp95を返さない。
人手で曖昧とされたholdoutは成功にも失敗にも含めず、曖昧件数と評価者一致度として残す。
結果が悪いvariantも削除しない。
