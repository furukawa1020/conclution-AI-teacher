# 意図的割込みの逐次証拠ゲート

## 境界

AI再生中の割込み候補は、既存の720ms foreground / 1,200ms quiet判定を正本として維持する。その上に、AECがブラウザ設定から明示的に確認でき、かつRustの強いguard閾値を通った音だけを対象に、400〜520msのfast laneを追加する。

Rust/Wasmは各40ms観測を `None / Voice / Foreground / ForegroundChange` の有限enumへ落とし、次の有限状態だけを返す。

```text
Armed → Candidate → Provisional → FastReady → Confirmed
                               ↘ LegacyOnly
          下側境界または80ms超gap → Discarded
```

`LegacyOnly` と `Discarded` は発話を捨てる指示ではない。fast laneの証拠だけを不可逆に閉じ、JavaScript側は従来の720/1,200ms密度判定を継続する。AEC未確認・途中喪失、弱いforeground、520msまでに上側境界へ届かない候補も同じ従来経路へ戻る。

## 端末外へ出さない情報

ゲートの永続状態は、型付きphase、上下clipした固定小数点score、foreground時間、change回数、gap時間、直前の有限bucket、直前の候補経過時刻だけである。PCM、RMS/peak列、特徴量列、文字起こし、意味推定は保存も送信もしない。確認前PCMの所有権とzeroizationは既存のRust `PcmRing` generation capabilityから移さない。

## 受理証明

- fast confirmはAEC verified、400ms以上、foreground 320ms以上、2段以上のchange-pointを3回以上、SPRT型score上側境界、gap 80ms以下を同時に要求する。
- sample clockの重複・逆行、未知phase/signal、非有限値、範囲外counterは例外としてfail-closedになり、証拠を増やさない。
- cough、impulse、相づち、quiet/constant/sparse mutter、playback leakage、AEC喪失を含む制約付きH0 synthetic 100,000 traceでfast confirmは0件とする。0件時の二項分布片側95%上限は0.5%未満である。
- 高確度intentional synthetic群のdecision p95は520ms以下、従来720msから200ms以上短いことをテストで固定する。
- 配布する同一のraw Wasm exportを実Chromeから1 frameずつ512回呼び、update p95が0.2ms/frame以下であることをrelease-only gateで固定する。production AudioWorklet起動時はこのbenchmarkを実行せず、有限self-testを1回だけ行う。

このsynthetic上限は、実利用者全体の誤割込み率や発話意図の証明ではない。実端末分布は別の同意付き・本文なし計測で評価する。
