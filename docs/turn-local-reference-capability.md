# turn-local参照capability

## 目的

Issue #152のspeaker embedding-free target speech extractionへ渡す参照を、モデルより先に安全な有限所有物へする。USEF-TPは固定speaker embeddingの代わりに参照音声とのframe-level cross-attentionを使うが、参照の寿命と取得条件はプロダクト側で別に保証する必要がある。

- 論文: https://arxiv.org/abs/2501.03612
- 公式実装: https://github.com/ZBang/USEF-TSE

公式実装はCC BY-NC 4.0であり、商用プロダクトへcheckpointやコードをそのまま組み込まない。本実装は論文のモデルを移植せず、後続のライセンス適合モデルが利用する参照capabilityだけを独立に実装する。

## Rust境界

`TurnReferenceWindow` は現在turnの正のgenerationとAudioContext sample rateへ束縛する。16kHz PCM16の20ms frameから8つの固定帯域featureを計算するが、PCMとfeatureの読出しAPIは持たない。

収集には次の条件をすべて要求する。

- quiet onsetが確認済み
- browser AECが明示的に確認済み
- AI音声を再生していない
- 話者重なりが検出されていない
- sample clockが単調で、観測間隔が100ms以下
- constructorと同じgeneration

20 frame、400msで`Ready`になる。それより前は`Collecting`であり、21 frame目、stale generation、AEC不明、再生中、overlap、時計逆行、無音・不正長は`Unresolved`へ遷移して全featureとcounterをzeroizeする。`clear`とDropも同じデータをzeroizeする。

JavaScript/Wasmへ公開するのは`Collecting / Ready / Unresolved / Cleared`の有限phaseと0〜20のcountだけである。PCM、帯域値、speaker embedding、声紋、本文、文字起こしは公開しない。

## release証明

- Rust: 400ms境界、21 frame拒否、全曖昧条件、10万H0でReady誤到達0
- Wasm: `turnReferenceBoundarySelfTest`
- AudioWorklet runtime: 初期化時に自己検査がtrueでなければfail-closed
- 実Chrome: 配布版pcm-ring Wasmを直接instantiateし、既存`observationAddingValidated` gate内で同じ自己検査を必須化

このPRでは参照capabilityをASRや抽出音へ接続しない。通常raw経路とlatencyは変更せず、実cross-attention、model digest/license/SBOM、raw/extracted一致proof、CER評価は#152の後続PRで行う。
