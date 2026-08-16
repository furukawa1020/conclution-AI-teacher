# 小声入口の帯域証拠境界

## 目的

「小声が届いた」という表示ではなく、声量を上げられない利用者の音響入力を実際に発話開始へ接続する。同時に、空調・マイクの自己雑音・一定音を発話として送信しない。

## 所有権

`kotae-audio-core` の `QuietEvidenceTracker` が次を単一遷移として所有する。

- 250 / 500 / 1000 / 1800 / 2800 / 4000 Hz の六帯域エネルギー
- 各帯域の速い雑音床と遅い雑音床
- 前フレームに対する正の spectral flux
- 発話候補中の 400 ms 雑音床凍結
- Web Audio の単調 sample clock

JavaScript はPCMや独自閾値から小声判定を再構成しない。Rustが返す固定フラグと有限な雑音床だけを通常VADへ渡す。PCMはWasm呼び出し後に保持せず、文字起こし・本文・周波数特徴をイベントやログへ出さない。

## 受理と拒否

小声候補には、局所SNRが立つ帯域が二つ以上、正のspectral flux、発話帯域形状をすべて要求する。一定の低レベル音は `stationary` となり候補にならない。候補中は床の追従を止め、小声そのものを雑音として学習して消すことを防ぐ。

時刻の重複・逆行、5秒を超える観測欠損、非有限PCM、範囲外のWasm結果はfail-closedでその録音を拒否する。JavaScriptの遅延一回を発話継続時間として水増しせず、継続時間は既存のRust sample-clock遷移が最大40 msだけ加点する。

## release証拠

- 定常音10万フレームで小声候補への誤受理が0件
- 変化する低レベル多帯域信号を候補として受理
- 候補中に雑音床が凍結される
- 配布版client Wasmの `quietSubbandEvidenceSelfTest` を実Chromeで直接実行
- `quietSubbandEvidenceValidated=true` がないHosting releaseを拒否

これは実環境コーパスの代替ではない。端末・距離・空調条件を固定した実小声コーパスのrelease gateは Issue #97 で別に管理する。
