# 質問拘束・単回inline PhraseSet

Issue #102 の第一境界は、Cloud Speech-to-Text V2へ永続PhraseSet resourceを作りません。許可する語は、画面に既に出た現在質問の有限語彙と、認証済みstateが「本人が以前に発した」と証明できる有限語彙だけです。期待回答、模範解答、LLMの候補、PDF本文、会話要約を入力源にしてはいけません。

`QuestionBoundPhraseSet` は現在質問のSHA-256、turn generation、最大5分の期限へ束縛されたprocess-local capabilityです。質問語8件、本人語8件、重複除去後16件、1語24 rune、合計192 runeを上限にします。改行・制御文字、空digest、未知generation、期限超過を拒否し、成功・失敗を問わず一度しか利用できません。PhraseSetはinline形式だけで、resource name、display name、custom class、phrase別boostを持たず、全体boostを4に固定します。

認識は次の順序です。

1. 既存の東京 `chirp_3` 設定でbaselineを一度だけ認識する。
2. baselineがconfidence 0.65以上なら、その結果を返してadapted requestを送らない。
3. baselineが空または不足した時だけ、同じ音声を単回inline PhraseSet付きで認識する。
4. adaptedも0.65未満なら認識missへ倒す。
5. baselineとadaptedがどちらも非空なのに異なる場合は、どちらも選ばず`question_bound_phrase_set_unresolved`へ倒す。

本文、phrase、question digest、generation、認識仮説をlog、telemetry、永続stateへ出しません。この段階はprovider requestの安全境界であり、production voice経路への接続は、現在質問と本人由来語彙を発行する認証済みcapabilityをreader-firstで導入した後に行います。
