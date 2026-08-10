# Question-Bound Answer Ownership Proof（QBA Proof）

QBA Proofは、KOTAEの明示回答支援にだけ使う、本文を含まない現在turn限定のサーバー判定です。

> 入力内で報告された問いと、AIが作った文ではない確定入力内の答えが結び付き、その答えが先に出た。

汎用チャットの応答品質や事実正確性を証明する仕組みではありません。第三者が現実にその質問をした事実、現在の話者、本人の法的身元、声紋、録音の真正性、STTの正確性、回答の正解、能力、上達も証明しません。

## 発行不変条件

次の条件をすべて満たす同一turnだけに、`question_bound_input_answer_first`を返します。一つでも確認できなければ`none`です。

1. 入力は音声pipelineがfinal境界を越えた`committed_voice`であり、暫定認識やspeculative model callではない。speculative中の候補は、最終transcriptが完全一致して採用された場合だけ引き継ぐ
2. 利用者が、入力内で「第三者から聞かれた」と報告した質問への支援を明示している
3. 同じ解析済み質問spanから質問主題と決定論的なoperatorを一意に取り出せ、そのoperatorがplannerと一致する。質問span全体のHMAC、screen済み質問主題のHMAC、全required-slot evidenceのHMACが、認証済み短期stateの同じscopeにある
4. 回答evidenceは現在の入力発話に完全一致する連続spanであり、authoritativeな回答試行の60%以下で、AI draft、reconstruction、質問を含むturn全体にしか成立しない語を含まない
5. 決定論的Meaning Gateが全required slotのcoverage=1、target satisfied、A-firstと判定する
6. 別model callのLAC criticもcoverage=1、target satisfied、A-firstと独立に判定する
7. 新しい別質問、引用、代理回答、訂正・撤回、別人を主語にした答え、無関係な次turn、監査timeoutではない
8. 厳格モードでもPDF turnでもない

質問インスタンス、質問主題、回答evidenceは、同じ鍵素材から用途分離したHMACへ変換します。raw tag自体もwireへ出しません。cross-turn stateにはoperator、required slot、有限の制御値、非可逆tagだけを入れ、UID-bound AES-256-GCMで暗号化し、15分で失効させます。

## Wire contract

HTTP、NDJSON stream、WebSocket finalは同じ有限値を返します。

```json
{"answerProof":"none"}
```

```json
{"answerProof":"question_bound_input_answer_first"}
```

verified値を許すmetadataは次の二形だけです。

```text
respondent / restructure / complete / complete
respondent / restructure / expanding / expand
```

後者は、A-firstを確認した直後に、利用者が事前に明示した任意の一問へ進む場合です。その次のexpansion回答をもう一度合格判定するものではありません。

complete / completeでverifiedになったturnは回答所有権yieldとし、KOTAEは相づち・講評・成功音声を合成しません。HTTPとNative caption handoffのどちらも空のspoken replyによってTTSを呼ばず、暗号化stateと固定proof enumだけをfinalへ載せます。画面はこの組を「AIの返答が失敗した無音」と区別し、現在turnだけ回答所有権レシートとして表示します。expanding / expandは利用者が事前に明示した一問を音声で返すためyieldではありません。

ブラウザは未知値とmetadata不整合を拒否します。proofはvalidated finalからだけRust stateへ入り、forge可能なCustomEventからは設定しません。次の発話開始がVADで確定した時点でproofだけを直ちに消し、会話制御stateは必要な範囲で維持します。確定前の短いぼやきでは消去もinterruptionもしません。interruption済みfinalでは、すでに次の発話が始まっているため古いproofを表示しません。

## 発行しない代表例

| 入力 | 結果 | 理由 |
|---|---|---|
| 「理由は費用です。結論はAです」 | `none` | A-later |
| AI draftにはAがあるが本人発話にはない | `none` | exact spanなし |
| 必須の単位・条件・不確実性が欠ける | `none` | required slot不足 |
| 同じ主題だが別の質問へ移った | `none` | question instance不一致 |
| 「田中さんはAと言っています」 | `none` | proxy answer |
| 「Aです、いやBです」 | `none` | 訂正・矛盾 |
| critic timeout / 不一致 | `none` | 二重検証不能 |
| Native Audioの通常会話 | `none` | 明示回答支援scope外 |
| 厳格モードまたはPDF turn | `none` | proof監査scope外 |
| QBA Proof後の次turn | `none` | current-turn限定 |

## 正確に言える比較範囲

KOTAEがあらゆる会話で汎用音声AIより賢い、とは主張しません。QBA Proofが狙う差は狭く、検証可能です。

> 入力内で第三者から聞かれたものとして報告された質問への支援で、AIが回答を代作せず、確定入力内のA-firstだけを、質問・回答本文をKOTAEの履歴へ残さず現在turnに表示する。

公開比較で「上回った」と数値主張する場合は、同一音声・同一台本・固定versionのblind評価を別途行います。主要指標は、本人がAを話す前のAI代答率、false-complete率、別質問／proxyの漏洩率です。少なくともoperator別の正常A、A-later、無A、引用、訂正、無関係話題、短いぼやきを含めます。
