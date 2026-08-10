# QBA-Δ（同じAの後ろ→先頭遷移証明）

QBA-Δは、明示回答支援の連続する2 turnだけを対象にした、本文を含まない現在turn限定の証明です。前turnで本人由来の同じAが後ろにあり、現turnではそのAが先頭へ移った時だけ、`question_bound_input_clause_later_to_first`を返します。それ以外は常に`none`です。

前turnでは、決定論的Meaning Gateと独立LAC criticの双方が、全required slotを含むA-laterで一致している必要があります。暗号化stateへ保存するのは、その一致を表す有限enumと、既存の質問instance・質問continuity・exact A clauseの用途分離HMACだけです。質問、回答、文字起こし、音声、evidence span、AI再構成文は保存しません。

現turnでは、確定音声、同じ質問instance・operator・required slots、同じexact A clause tag、両検証器のcoverage=1かつA-first、通常QBA Proofの全条件が必要です。proxy、引用、訂正、別質問、改ざんstate、暫定caption、timeout、PDF、strict modeではfail-closedで`none`になります。初回A-first、無AからA、Aを変更した言い直し、paraphraseも遷移証明にはしません。

証明成立turnは回答所有権yieldです。spoken reply、TTS、PCM、相づち、成功音を出しません。画面には今回だけ、スコア・streak・レベルなしで次を表示します。

> 同じAの一文が、後ろから先頭へ移りました  
> AIは答えを足していません

これは正解、本人性、話者ライブネス、長期上達、他場面への転移、因果効果を表しません。

## 段階rollout

1. reader: 両flagを`false`にし、未知値と余分fieldを拒否できるreaderだけを全trafficへ配る
2. writer: `KOTAE_ANSWER_TRANSITION_WRITES=true`、behaviorは`false`のまま、有限evidenceだけを暗号化stateへ発行する
3. behavior: writerが全trafficで安定した後、`KOTAE_ANSWER_TRANSITION_ENABLED=true`にして現在turnのproofとUIを有効にする

behavior flagはwriter flagと既存の質問拘束済みretrieval policyを必須とします。writerを戻す時はbehaviorを先に無効化します。

比較指標は`Owned A-first completion`です。同一質問・同一初回A-laterを用い、順序無作為化paired crossover、固定ChatGPT version、fresh chat、custom instructions無効、blind評価、収集前の事前登録を必須にします。paired差の95%信頼区間下限が0を超えた場合だけ、限定した比較優位を公開できます。
