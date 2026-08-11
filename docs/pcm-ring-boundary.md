# AudioWorklet PCM ring boundary

AI応答への割込み候補と、通常のlive入力が確定する前のPCMは、DOMやFirebaseへ依存しない専用の`kotae-pcm-ring` Rust/Wasm moduleが所有します。`audio_core`は本文も原音も扱わない音響判定境界なので、PCM所有権をそこへ混在させません。

## 有限性と所有権

- 1 frameは16 kHz PCM16 monoの20 ms、正確に640 byteです。
- 割込み前ringは最大125 frame（2.5秒、80,000 byte）、送信待ちqueueは最大200 frameです。
- constructorが固定`Slot { occupied, context_frame, pcm: [u8; 640] }`をcapacity分だけ一度確保します。push、eviction、shiftではheap allocationしません。
- `context_frame`は0以上のJavaScript safe integerかつ、同じring内で厳密に増加する値だけを受理します。重複・逆行はfail-closedです。
- confirm前はPCMをmain threadやnetworkへ返しません。confirm後もcreditのあるframeだけを一つずつAudioWorkletから移します。
- eviction、cutoff discard、shift、clear、stop、overflow、fatal、seal完了、Rust `Drop`で保持slotをzeroizeします。terminal経路は`clear`してからWasm handleを一度だけ`free`します。
- 原音、特徴量、発話本文をStorage、Firestore、log、telemetryへ保存するAPIはありません。

AudioWorklet内のzeroize済みmemoryを外部へ公開するtest APIは設けません。Rust unit testがslotをwhite-box検証し、実ブラウザtestはconfirm前の非送信、固定上限、eviction済みsentinelの非流出、stop後と次generationへの非流出、同一AudioContext内でfree後に新しいprocessorを生成できることをblack-boxで検証します。

## 失敗境界

main threadは同一originから25 KiB前後の専用Wasmを取得・compileし、`WebAssembly.Module`を`processorOptions`でAudioWorkletへ渡します。配布するWorkletは、監査対象のwasm-bindgen同期glue、ring runtime、capture processorをbuild時に決定論的に一つへ束ねたself-contained moduleです。AudioWorkletGlobalScopeにない`TextDecoder`や非同期loader、外部JavaScript importを含む成果物はbuildで拒否し、中間glueとruntimeは公開しません。

runtime、module、ABI、control、sample clock、ring操作のどれかが不正ならJavaScript ringへfallbackせず、保持PCMを消去して一度だけ本文なしの`capture_invalid`を返します。これによりPCM handoffだけを破棄し、既存のbounded MediaRecorder fallbackと再生継続の所有権を壊しません。

## 検証

ローカルのsource回帰は次で実行します。

```powershell
cargo test -p kotae-pcm-ring --locked
node --test apps/client/tests/*.test.mjs
powershell -ExecutionPolicy Bypass -File scripts/build-web.ps1
node scripts/test-browser-audio.mjs --dist dist/web
```

release時はcleanな`origin/main`の40桁commitを`build-web.ps1 -ExpectedGitCommit`とbrowser gateの両方へ渡します。deploy processが保持するmanifestそのもののSHA-256と、Chromeが実際に読み取って検証したmanifestのSHA-256も一致しなければ配信しません。これにより並行buildで`dist/web`が差し替わっても、検証したartifactと異なるsnapshotをuploadできません。Chrome不在、processor error、console error、timeout、cleanup失敗はskipせず失敗です。

この境界はPCM ringの所有権だけを移します。720 ms / 1,200 msの割込み確認、通常VAD、長い独話と自然な間の時系列FSMは挙動を変えず、Issue #50で単調なAudioContext sample clockへ束縛したRust engineへ移します。Issue #50が完了するまで親Issue #18は閉じません。

## generation capability

各ringは正のJavaScript safe integerであるcapture generationをRust constructorで一度だけ束縛します。`push`、`count`、`shiftInto`、`clear`は同じgenerationを毎回提示した場合だけPCM所有状態へアクセスできます。stale generationは既存slotを読まず、追加・削除・clearを行わず、JS fallbackへ切り替えずfail-closedになります。

AudioWorklet用runtimeはring作成直後、異なるgenerationを使ったdirect-Wasmの`push`、`count`、`shiftInto`、`clear`がすべて無変異で拒否されることを確認します。probeとdestinationは確認後にzeroizeします。この自己検査を通らないWasm ABIではcapture processorを開始しません。実Chrome fixtureは全processor生成でこの自己検査を通過したことを必須結果として検証します。
