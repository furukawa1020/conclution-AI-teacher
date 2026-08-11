# Temporal VAD の sample clock 境界

通常発話と割込み発話の時間判定は、`setInterval` の呼出し回数ではなく、Web Audio の `currentTime × sampleRate` から得る単調な整数frameを正本にする。

Rust `audio_core` の有限遷移は、開始frame・直前frame・現在frame・sample rateだけを受け取る。現在frameが同一または逆行した場合、未対応sample rate、safe integerでないframe、Wasm未初期化、不正なWasm返却値はすべて fail-closed とする。生PCM・文字起こし・発話内容はこの境界へ渡さない。

1回の音響観測へ加算できる発話証拠は40msを上限とする。メインスレッドが停止して次のcallbackが遅れても、720msのforeground確認や1.2秒のquiet確認を1回の観測で飛び越えない。一方、候補2.4秒、反射的発話2.2秒、長い独話5秒、最大録音210秒などの経過時間はsample clockの実時間で進むため、JS timerの発火数には依存しない。

実Chrome gateでは、配布するRust/Wasmの同じ`audio_core`遷移をAudioWorklet内で直接self-testし、40ms credit上限、経過時間、重複・逆行拒否を確認してからPCMリングを生成する。失敗時はWorklet初期化自体を拒否する。
