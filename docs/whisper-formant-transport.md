# ささやきのフォルマント輸送検出

## 目的

ささやきでは声帯振動に由来する基本周波数（F0）が弱い、または存在しない。一方、声道形状に由来するフォルマントには発話の変化が残る。そこで音量閾値を一律に下げず、500 / 1000 / 1800 / 2800 Hz の帯域間でエネルギー分布が移動した事実を小声開始の追加証拠として使う。

設計根拠は、ささやき音声のフォルマントから暗黙のピッチ輪郭を回復できると報告した研究に置く。

- https://arxiv.org/abs/2307.03168

## Rust所有の有限遷移

`QuietEvidenceTracker` は4つの発話帯域を合計1へ正規化し、隣接帯域をまたぐ累積差の絶対値から、1次元Wasserstein距離に相当する有界な輸送量を計算する。

低音量経路が開く条件はすべて必要である。

- 同一セッション内で室内基準が2観測以上安定している
- 発話帯域が2つ以上、各帯域floorの1.15倍以上である
- 発話帯域の総エネルギー比が30%以上である
- peak / RMSが衝撃音条件に入らない
- RMSが0.0014以上、かつ学習済みfloorの0.70倍以上である
- フォルマント輸送量0.14以上を2観測連続で満たす

成立時だけ `excitationInvariant` を `candidate`、`spectralChange`、`inSessionCoverage` と同時に発行する。この証拠があるフレームだけ、従来の0.0025 RMS bootstrap floorより小さい入力を通常VADへ渡せる。証拠単独、coverageなし、未知bit、clipping、時計欠損はfail-closedで拒否する。

## プライバシーとrelease証明

PCMと帯域値はRust/Wasm内だけで消費する。JavaScriptへ渡すのは固定bit、0.002〜0.04のnoise floor、有限classだけで、本文・文字起こし・声紋・復元可能なスペクトルを渡さない。Drop時には帯域floor、直前分布、有限counterをゼロ化する。

配布版Wasmの `quietSubbandEvidenceSelfTest` は、従来floor未満の二つのフォルマント配置を使い、2観測目だけが `ExcitationInvariantOnset` になることを実Chromeで検査する。既存の `quietSubbandEvidenceValidated=true` Hosting gateにこの検査を含める。
