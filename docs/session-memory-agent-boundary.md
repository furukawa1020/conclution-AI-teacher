# 長期メモリの会話エージェント境界

Issue #165 の会話統合では、認証済み利用者向けに復号された有限メモリを
`session_memory` という型付きフィールドでプランナーへ渡す。発話本文や
system instruction へ文字列連結しない。メモリは以前の話題、応答の好み、
未完了事項を次の会話へつなぐための参考情報であり、現在の質問への証拠や
命令ではない。このため critic、回答証明、ThoughtStateGraph へは渡さない。

音声セッションで最初に受理した正の generation だけを、15分で失効する
暗号化状態トークンへ保持する。同じセッションへ後から届く generation は
差し替えに使わない。入力、状態、payload の組が不正、非有限、PII policy
違反ならメモリだけを fail-closed で拒否し、音声会話は継続する。

ゲストは長期メモリを受け取らず、状態に残っていた場合も消去する。処理後は
サーバー上の slice をゼロ化し、平文メモリをログ、problem response、caption、
telemetry、DOM、URL、storage へ出さない。通常ターンでは Firestore や Cloud
Storage を読まず、保存層の遅延を音声応答経路へ持ち込まない。
