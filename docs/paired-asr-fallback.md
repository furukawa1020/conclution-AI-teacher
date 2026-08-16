# 原音・強調音 paired ASR fallback

Issue #123 は、Rust が小声と確認したturnでNative / live経路が失敗した時だけ、
原音baselineとObservation Adding後のchallengerを独立したChirp 3 streamで認識する。
画面文言やLLMの推測で「聞こえた」ことにはしない。

## 所有権

- AudioWorkletは強調前PCMをbaseline用Rust ring、強調後PCMをchallenger用Rust ringへ保存する。
- 両ringは同一generation、同一contextFrame、同一frame数でなければ一byteもtransferしない。
- transferは最大1,500 frame（30秒）かつ一度だけである。上限到達時はpaired fallbackだけをzeroizeし、正常なlive主経路は止めない。
- bridgeは両ArrayBufferを同時に受け、長さ一致を確認し、base64化後に元bufferをzeroizeする。
- HTTPは`audio/l16`の時だけ`baselineAudioBase64`を許し、両経路を640 byte frame境界・同じ長さ・各30秒以下へ制限する。

## 認識commit

Cloud Runは同じ東京リージョン、同じChirp 3設定でbaselineとchallengerを並列decodeする。
空白列のcanonical化を除き、Unicode normalization、case folding、句読点除去、編集距離、
LLM補完は使用しない。二つの全文がbyte-for-byte一致した時だけ本文をsemantic inferenceへ渡す。
片側no-speech、provider failure、追加・削除・置換、不一致は固定のrecognition missとなる。
confidenceは両方が提供した場合の小さい方だけを後段risk controllerへ渡し、片側未提供なら
coverage未提供の0を保つ。

このトランシェは小声HTTP fallbackのhallucination防止であり、Native liveの二経路token区間commit、
小声CER改善、実コーパスcoverageを完了したとは主張しない。それらはIssues #109、#97、#117に残る。
