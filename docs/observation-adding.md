# 小声強調の原観測保存境界

## 目的

小声を単純に大きくするのではなく、強調処理が削った母音・子音遷移を原観測から戻してASRへ渡す。知覚上の綺麗さや最大SNRではなく、本人が発した音響情報の保存を優先する。

## Rust単一遷移

`kotae-pcm-ring` は、quiet proofで確定した20 ms PCM16 frameだけに次を実行する。

1. DC block・有限pre-emphasis・headroom付きgainでenhanced候補を作る。
2. raw観測とenhanced候補の正規化相関と残差エネルギー比を測る。
3. 歪みが大きいほどraw混合率を30%から40%の有限範囲で増やす。
4. 混合後peakを82%以下へ制限し、最終PCMだけを既存ringへ戻す。

相関が0.35未満、残差比が16を超える、非有限値、無音、通常音量、peak上限の場合は入力byte列を変更しない。JavaScriptは混合率や音響特徴を受け取らず、Rust/Wasmの成功・失敗だけを扱う。

## 保存しないもの

- raw/enhanced PCMの複製
- spectrum、相関、残差、音素、transcript
- speaker embedding、人物属性、感情・疾患推定

frame-local配列は返却前にzeroizeする。generation不一致、clear、Dropでもringとfilter stateを消去する。

## release証拠

- 無音と通常音声10万frameがbyte-stable
- 相関崩壊時にrawへbyte-stable fallback
- quiet formantの低域支持を失わず高域子音支持を改善
- stale generationが入力もstateも変更しない
- 配布Wasmの `observationAddingSelfTest` を実Chromeで直接実行
- `observationAddingValidated=true` がないHosting releaseを拒否

これは学習済みSSL enhancerそのものではなく、Issue #108で今後接続するenhancerが越えられないproduction安全境界である。raw/enhanced二経路ASRのtoken採用はIssue #109、実日本語小声コーパスはIssue #97で別に検証する。
