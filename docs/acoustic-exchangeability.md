# 端末内acoustic exchangeability gate

Issue #128は親Issue #126の第一トランシェとして、小声を許可する前に現在のAudioContext内で本文非依存の
音響基準が安定していることをRustで確認する。画面文言、質問、期待回答、文字起こし、
speaker embeddingは使わない。

`QuietEvidenceTracker`は最初のstationary観測だけから二時間尺度noise floorを作り、
連続した有限観測でsession coverageを確立する。coverageがない間、quiet candidate bitは
発行されない。0.5%以上のclipping、500msを越えるsample-clock gap、持続するnoise-floor
shiftはcoverageを失効させ、再び安定するまで小声経路を開かない。PCMと6帯域energyは
Rust呼出しの外へ返さず、JavaScriptが受け取るのは既存flags内の1 bitとnoise floorだけである。

この境界はpopulation-level conformal coverageを主張しない。未知device/room holdout、
署名済みcalibration artifact、MMD/energy-distance上界は親Issue #126と#117に残す。
本トランシェが証明するのは、現在sessionの基準が未確立・clipped・clock不連続な時に
「小声が届いた」と誤ってcommitしないことだけである。

配布client Wasmは`acousticExchangeabilitySelfTest`を公開し、実Chrome release gateが
未coverage、安定後のquiet admission、clipping後の即時revokeを同じRust遷移で検査する。
