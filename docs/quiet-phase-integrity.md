# 小声強調の複素位相・因果整合境界

小声強調は音量を上げるだけでは安全ではない。語頭子音が原音より先に現れたり、時間方向へ伸びたりすると、ASRに存在しない音素を渡し得る。この境界は強調音を「生成された正解」として扱わず、同じ20 ms原音から因果的に支持された観測かだけを端末内Rustで検査する。

`kotae-pcm-ring` は次を単一遷移で計算する。

- Hann窓を用いた6帯域の複素射影
- raw/enhanced cross-spectrumの位相差
- 隣接帯域間の群遅延
- rawより先行するenhanced onset
- raw終了後へ広がるtemporal smear
- raw/enhancedの有限lag相互相関

結果は `invalid / consistent / insufficient_support / non_causal_onset / excess_group_delay / temporal_smear` の固定enumだけである。複素スペクトル、PCM、周波数係数、文字起こし、speaker特徴はRust外へ出さず、clear・generation交代・dropで履歴をzeroizeする。

`consistent` 以外の強調結果は原音へ戻す。位相を補正して音を作る処理は行わない。Wasmはgeneration capabilityに束縛されたenumを返し、AudioWorkletは強調済みresultに `consistent` が伴わなければfail closedする。

release gateは配布Wasmの `observationAddingSelfTest` を実Chromeで反復し、正常な小声強調、非因果onset拒否、20 ms frameのp95上限を同じRust遷移で検査する。
