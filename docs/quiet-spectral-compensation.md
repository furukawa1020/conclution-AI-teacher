# 小声ASR向け周波数補償境界

この境界は「小さな声で届いています」という表示を出す機能ではありません。Rust VAD が小声発話と確定した後の 16 kHz・mono・PCM16・20 ms フレームそのものを、同じ Rust/Wasm 所有境界内でASR向けに補償します。

## 有限DSP

- 対象は `quietConfirmed=true` かつ候補開始sample-clock以後のフレームだけです。
- 0.0015 RMS未満の無音域、0.055 RMS以上の通常音声、0.82以上のpeakはbyte-stableで通します。
- 対象フレームにはpole 0.995のDC blockerと係数0.28のpre-emphasisを適用し、適応gainは最大4倍、出力peakは0.82以下に制限します。
- gain、前入力、DC履歴はgeneration capabilityへ束縛されます。世代違い、長さ違い、非有限計算はPCMを変えずfail-closedします。
- 一時スペクトル配列、リング内PCM、DSP履歴はclear・drop・失敗経路でzeroizeします。音声内容、PCM、係数推定値をログやUIへ出しません。

同じ補償済みフレームをNative streamingとHTTP fallbackの両方へ渡すため、経路ごとに認識入力が変わりません。通常音声と無音を変えないこと、ささやき相当の合成信号で高域/低域比が増えること、peak上限、stale generation無変異をRustテストで固定します。配布Wasmを使う実AudioWorklet検証もrelease gateに含めます。

## 効果の扱い

この実装が固定するのは安全な信号処理境界です。日本語小声・ぼやきの認識率改善は、Issue #97の実音声コーパスでWER/CER、取りこぼし、誤起動を測り、改善しない場合はreleaseを止めます。表示だけで「認識できた」と代用しません。
