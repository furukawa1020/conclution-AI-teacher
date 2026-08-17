# A-firstゲスト体験の30秒SLO

ゲスト体験が単なる会話デモに戻らないよう、利用者が自分で答えを作る一連の流れを、配布物と同じブラウザ経路で検証する。

## 固定する境界

- ゲスト開始から録音が実際に所有された時点まで: 1秒以内を目標とする。
- 利用者の最後の有声フレームからWeb Audioの最初の可聴サンプルまで: 1秒以内を目標とする。
- ゲスト開始から、質問に束縛されたA-first回答の証明まで: 30秒以内とする。
- 100観測中95観測以上が二つの1秒境界を満たし、全観測が30秒以内かつAI代答なしの場合だけ、バッチを合格にする。

時刻は同一ブラウザの単調時計でその場で分類し、生の時刻・遅延値・音声・文字起こし・UIDはイベントへ出さない。イベントは固定enumだけを持つ。

## ゲスト開始の認証境界

fresh browserの初回statusはFirebase AppとローカルAuthだけを初期化し、Auth状態が空なら`identity-required`を返す。この経路ではApp Checkオブジェクト自体を作らない。reCAPTCHA Enterprise評価は「30秒で違いを試す」という明示gestureの後、匿名Authより前に一度だけ開始する。これにより、gesture前の低スコアがSDKの長いthrottleへ入り、後のクリックを無効化する経路を持たない。

開始attemptはpositive safe integer generationへ束縛し、App Check、Auth初期化、persistence切替、匿名sign-inの各`await`後に同じgenerationを検証する。12秒を越えたattempt、pagehide、二重開始、別attemptへ差し替わった完了はguest sessionを有効化しない。匿名sign-inだけが遅れて完了した場合は、そのidentityをsign-outして破棄する。App Check enforcementと匿名利用者専用rate limitは維持する。

## 反例

「Aがない」「別の質問へのA」「引用されたA」「回答順序が変わっていない」「AIが先に答えた」「未知フィールドを足した」観測は、速くても成功に数えない。回答本文ではなく、サーバーが発行しクライアントがfail-closedで検証した質問束縛proofと遷移proofを使う。

## リリースゲート

制御時計の100観測と反例群をNodeで検証し、同じpolicy moduleをrelease artifactから実Chromeへ読み込む。実Chromeの `guestAFirstSprintSloValidated` が真でない配布物はFirebase Hostingへ進めない。

このSLOは体験品質の回帰検知であり、個人の能力評価や医療的な効果測定ではない。
