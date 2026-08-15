package config

import (
	"os"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guard"
)

func setTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("KOTAE_ENV", "test")
	t.Setenv("KOTAE_ALLOW_INSECURE_DEV", "true")
	t.Setenv("KOTAE_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_VOICE_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_VOICE_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_GUEST_VOICE_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_GUEST_VOICE_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_GUEST_VOICE_APP_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_GUEST_VOICE_APP_RATE_LIMIT_PER_DAY", "")
	unsetTestEnvironment(t, "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE")
	unsetTestEnvironment(t, "KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY")
	unsetTestEnvironment(t, "KOTAE_RETRIEVAL_BELIEF_WRITES")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", "")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", "")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", "")
	t.Setenv("KOTAE_PASSKEY_RP_ID", "")
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "")
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "")
	t.Setenv("KOTAE_GUEST_MODE_ENABLED", "")
	t.Setenv("KOTAE_MAX_VOICE_BYTES", "")
	t.Setenv("KOTAE_SPEECH_MODEL", "")
	t.Setenv("KOTAE_SPEECH_VOICE", "")
	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "")
	t.Setenv("KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED", "")
	t.Setenv("KOTAE_NATIVE_AUDIO_LOCATION", "")
	t.Setenv("KOTAE_NATIVE_AUDIO_MODEL", "")
	t.Setenv("KOTAE_NATIVE_AUDIO_VOICE", "")
	t.Setenv("KOTAE_VERTEX_PRIORITY", "")
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "")
	t.Setenv("KOTAE_STATE_V2_WRITES", "")
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "")
	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "")
	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "")
}

func unsetTestEnvironment(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Errorf("unset %s: %v", key, err)
		}
	})
}

func TestLoadUsesConservativeRateLimitDefaults(t *testing.T) {
	setTestEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimits.PerMinute != guard.DefaultPerMinute {
		t.Fatalf("per-minute limit = %d; want %d", cfg.RateLimits.PerMinute, guard.DefaultPerMinute)
	}
	if cfg.RateLimits.PerDay != guard.DefaultPerDay {
		t.Fatalf("daily limit = %d; want %d", cfg.RateLimits.PerDay, guard.DefaultPerDay)
	}
	if cfg.VoiceAppRateLimits != (guard.Limits{
		PerMinute: guard.MaxPerMinute,
		PerDay:    guard.MaxPerDay,
	}) {
		t.Fatalf("voice app rate limits = %+v", cfg.VoiceAppRateLimits)
	}
	if cfg.GuestVoiceRateLimits != (guard.Limits{PerMinute: 4, PerDay: 16}) {
		t.Fatalf("guest voice rate limits = %+v", cfg.GuestVoiceRateLimits)
	}
	if cfg.GuestVoiceAppRateLimits != (guard.Limits{PerMinute: 20, PerDay: 200}) {
		t.Fatalf("guest voice app rate limits = %+v", cfg.GuestVoiceAppRateLimits)
	}
	if cfg.PasskeyClientRateLimits != (guard.Limits{PerMinute: 10, PerDay: 100}) {
		t.Fatalf("passkey client rate limits = %+v", cfg.PasskeyClientRateLimits)
	}
	if cfg.PasskeyAppCircuitBreaker != (guard.Limits{
		PerMinute: 300,
		PerDay:    20_000,
	}) {
		t.Fatalf("passkey app circuit breaker = %+v", cfg.PasskeyAppCircuitBreaker)
	}
	if cfg.PasskeyRPID != "kotae-ai.web.app" || cfg.PasskeyOrigin != "https://kotae-ai.web.app" {
		t.Fatalf("passkey RP = %q / %q", cfg.PasskeyRPID, cfg.PasskeyOrigin)
	}
	if cfg.RequireRecentPasskeyForVoice {
		t.Fatal("insecure development must default the recent-passkey voice gate off")
	}
	if cfg.GuestModeEnabled {
		t.Fatal("guest mode must default off")
	}
	if cfg.MaxVoiceBytes != 13*1024*1024 {
		t.Fatalf("max voice bytes = %d; want 13 MiB", cfg.MaxVoiceBytes)
	}
	if cfg.SpeechModel != "chirp_3" {
		t.Fatalf("speech model = %q; want chirp_3", cfg.SpeechModel)
	}
	if cfg.SpeechVoice != "ja-JP-Chirp3-HD-Kore" {
		t.Fatalf(
			"speech voice = %q; want ja-JP-Chirp3-HD-Kore",
			cfg.SpeechVoice,
		)
	}
	if cfg.NativeAudioEnabled {
		t.Fatal("native audio must remain an explicit deployment opt-in")
	}
	if cfg.NativeCaptionHandoffEnabled {
		t.Fatal("native caption handoff must remain an independent deployment opt-in")
	}
	if cfg.NativeAudioLocation != "us-central1" ||
		cfg.NativeAudioModel != "gemini-live-2.5-flash-native-audio" ||
		cfg.NativeAudioVoice != "Kore" {
		t.Fatalf(
			"native audio config = %q / %q / %q",
			cfg.NativeAudioLocation,
			cfg.NativeAudioModel,
			cfg.NativeAudioVoice,
		)
	}
	if cfg.VertexPriority {
		t.Fatal("Vertex priority must remain opt-in")
	}
	if cfg.CoachRestatementBinding {
		t.Fatal("restatement tag issuance must remain opt-in for staged rollout")
	}
	if cfg.StateV2Writes {
		t.Fatal("extended state writes must remain opt-in for staged rollout")
	}
	if cfg.VerifierProgressWrites {
		t.Fatal("verifier progress writes must remain opt-in for staged rollout")
	}
	if cfg.RetrievalPolicyEnabled {
		t.Fatal("retrieval behavior must remain opt-in for a separate canary")
	}
}

func TestLoadParsesTwoTierPasskeyLimits(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", "7")
	t.Setenv("KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", "70")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", "250")
	t.Setenv("KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", "15000")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PasskeyClientRateLimits != (guard.Limits{PerMinute: 7, PerDay: 70}) {
		t.Fatalf("passkey client limits = %+v", cfg.PasskeyClientRateLimits)
	}
	if cfg.PasskeyAppCircuitBreaker != (guard.Limits{PerMinute: 250, PerDay: 15_000}) {
		t.Fatalf("passkey app circuit breaker = %+v", cfg.PasskeyAppCircuitBreaker)
	}
}

func TestLoadRejectsLegacyEnvironment(t *testing.T) {
	for _, key := range []string{
		"KOTAE_PASSKEY_APP_RATE_LIMIT_PER_MINUTE",
		"KOTAE_PASSKEY_APP_RATE_LIMIT_PER_DAY",
		"KOTAE_RETRIEVAL_BELIEF_WRITES",
	} {
		t.Run(key, func(t *testing.T) {
			setTestEnvironment(t)
			// Presence itself is rejected, including a deceptively empty value.
			t.Setenv(key, "")

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) ||
				!strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("legacy migration error = %v", err)
			}
		})
	}
}

func TestLoadParsesRecentPasskeyVoiceGateStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireRecentPasskeyForVoice {
		t.Fatal("recent passkey voice gate was not enabled")
	}
	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "later")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE") {
		t.Fatalf("malformed gate error = %v", err)
	}
}

func TestLoadRequiresRecentPasskeyByDefaultOutsideInsecureDev(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_ENV", "production")
	t.Setenv("KOTAE_ALLOW_INSECURE_DEV", "false")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "kotae-test")
	t.Setenv("KOTAE_ALLOWED_APP_IDS", "1:123:web:abc")
	t.Setenv("KOTAE_STATE_KEY_BASE64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireRecentPasskeyForVoice {
		t.Fatal("production must require a recent passkey when the gate is unspecified")
	}

	t.Setenv("KOTAE_REQUIRE_RECENT_PASSKEY_FOR_VOICE", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequireRecentPasskeyForVoice {
		t.Fatal("an explicit false must remain available for a controlled migration")
	}
}

func TestLoadRequiresOneExactPasskeyOrigin(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_PASSKEY_RP_ID", "localhost")
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "http://localhost:3000")
	if _, err := Load(); err != nil {
		t.Fatalf("local exact origin rejected: %v", err)
	}
	t.Setenv("KOTAE_PASSKEY_ORIGIN", "http://attacker.invalid")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "KOTAE_PASSKEY_RP_ID") {
		t.Fatalf("mismatched RP error = %v", err)
	}
}

func TestLoadParsesStateV2WritesStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.StateV2Writes {
		t.Fatal("state v2 writes were not enabled")
	}

	t.Setenv("KOTAE_STATE_V2_WRITES", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_STATE_V2_WRITES") {
		t.Fatalf("malformed state v2 writes error = %v", err)
	}
}

func TestLoadParsesAnswerProofWritesStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AnswerProofWrites {
		t.Fatal("answer proof writes were not enabled")
	}

	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_ANSWER_PROOF_WRITES") {
		t.Fatalf("malformed answer proof writes error = %v", err)
	}
}

func TestLoadParsesVerifierProgressWritesStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VerifierProgressWrites {
		t.Fatal("verifier progress writes were not enabled")
	}

	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_VERIFIER_PROGRESS_WRITES") {
		t.Fatalf("malformed verifier progress writes error = %v", err)
	}
}

func TestLoadStagesAnswerTransitionWriterBeforeBehavior(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "true")
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "true")
	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "true")
	t.Setenv("KOTAE_ANSWER_TRANSITION_WRITES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AnswerTransitionWrites || cfg.AnswerTransitionEnabled {
		t.Fatalf("unexpected transition rollout: %+v", cfg)
	}

	t.Setenv("KOTAE_ANSWER_TRANSITION_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AnswerTransitionEnabled {
		t.Fatal("transition behavior was not enabled")
	}

	t.Setenv("KOTAE_ANSWER_TRANSITION_WRITES", "false")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "requires KOTAE_ANSWER_TRANSITION_WRITES") {
		t.Fatalf("behavior without writer error = %v", err)
	}
}

func TestLoadParsesAnswerTransitionFlagsStrictly(t *testing.T) {
	setTestEnvironment(t)
	for _, name := range []string{
		"KOTAE_ANSWER_TRANSITION_WRITES",
		"KOTAE_ANSWER_TRANSITION_ENABLED",
	} {
		t.Setenv(name, "eventually")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("malformed %s error = %v", name, err)
		}
		t.Setenv(name, "false")
	}
}

func TestLoadRejectsVerifierProgressWritesWithoutStateV2(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "true")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "requires KOTAE_STATE_V2_WRITES") {
		t.Fatalf("verifier progress without state v2 error = %v", err)
	}
}

func TestLoadSeparatesRetrievalPolicyFromStateWriter(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "true")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "requires KOTAE_STATE_V2_WRITES") {
		t.Fatalf("policy without reader/writer prerequisites error = %v", err)
	}

	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "true")
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RetrievalPolicyEnabled || cfg.VerifierProgressWrites {
		t.Fatalf("retrieval policy was coupled to verifier progress writes: policy:%v writer:%v", cfg.RetrievalPolicyEnabled, cfg.VerifierProgressWrites)
	}

	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_RETRIEVAL_POLICY_ENABLED") {
		t.Fatalf("malformed retrieval policy error = %v", err)
	}
}

func TestLoadRejectsRetrievalPolicyWithoutRestatementBinding(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "true")
	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "true")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_COACH_RESTATEMENT_BINDING") {
		t.Fatalf("policy without restatement binding error = %v", err)
	}
}

func TestLoadParsesNativeAudioGateStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NativeAudioEnabled {
		t.Fatal("native audio gate was not enabled")
	}

	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_NATIVE_AUDIO_ENABLED") {
		t.Fatalf("malformed native audio gate error = %v", err)
	}
}

func TestLoadParsesNativeCaptionHandoffGateStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "true")
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_ANSWER_PROOF_WRITES", "true")
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "true")
	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "true")
	t.Setenv("KOTAE_RETRIEVAL_POLICY_ENABLED", "true")
	t.Setenv("KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NativeCaptionHandoffEnabled {
		t.Fatal("native caption handoff gate was not enabled")
	}

	t.Setenv("KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED") {
		t.Fatalf("malformed native caption handoff gate error = %v", err)
	}
}

func TestLoadRejectsNativeCaptionHandoffWithoutRetrievalPolicy(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "true")
	t.Setenv("KOTAE_STATE_V2_WRITES", "true")
	t.Setenv("KOTAE_VERIFIER_PROGRESS_WRITES", "true")
	t.Setenv("KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED", "true")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "requires KOTAE_RETRIEVAL_POLICY_ENABLED") {
		t.Fatalf("caption handoff without retrieval policy error = %v", err)
	}
}

func TestLoadRejectsNativeCaptionHandoffWithoutNativeAudio(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_NATIVE_CAPTION_HANDOFF_ENABLED", "true")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "requires KOTAE_NATIVE_AUDIO_ENABLED") {
		t.Fatalf("caption handoff without native audio error = %v", err)
	}
}

func TestLoadPinsNativeAudioToItsGARegion(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_NATIVE_AUDIO_ENABLED", "true")
	t.Setenv("KOTAE_NATIVE_AUDIO_LOCATION", "us-central1")
	if _, err := Load(); err != nil {
		t.Fatalf("GA native-audio region rejected: %v", err)
	}

	t.Setenv("KOTAE_NATIVE_AUDIO_LOCATION", "global")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_NATIVE_AUDIO_LOCATION") {
		t.Fatalf("unsupported native-audio region error = %v", err)
	}
}

func TestLoadParsesCoachRestatementBindingStrictly(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CoachRestatementBinding {
		t.Fatal("restatement binding was not enabled")
	}

	t.Setenv("KOTAE_COACH_RESTATEMENT_BINDING", "eventually")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_COACH_RESTATEMENT_BINDING") {
		t.Fatalf("malformed restatement binding error = %v", err)
	}
}

func TestLoadAcceptsPriorityOnlyOnTheGlobalVertexEndpoint(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_VERTEX_PRIORITY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.VertexPriority {
		t.Fatal("Vertex priority was not enabled")
	}

	t.Setenv("GOOGLE_CLOUD_LOCATION", "asia-northeast1")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_VERTEX_PRIORITY") {
		t.Fatalf("regional priority error = %v", err)
	}
}

func TestLoadRejectsMalformedPriorityFlag(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_VERTEX_PRIORITY", "sometimes")

	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_VERTEX_PRIORITY") {
		t.Fatalf("malformed priority error = %v", err)
	}
}

func TestLoadAcceptsExplicitChirp3SpeechModel(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_SPEECH_MODEL", "chirp_3")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpeechModel != "chirp_3" {
		t.Fatalf("speech model = %q; want chirp_3", cfg.SpeechModel)
	}
}

func TestLoadAllowsThirteenMiBVoiceEnvelopeButNoMore(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_MAX_VOICE_BYTES", "13631488")
	if _, err := Load(); err != nil {
		t.Fatalf("13 MiB envelope rejected: %v", err)
	}

	t.Setenv("KOTAE_MAX_VOICE_BYTES", "13631489")
	if _, err := Load(); err == nil ||
		!strings.Contains(err.Error(), "KOTAE_MAX_VOICE_BYTES") {
		t.Fatalf("oversized envelope error = %v", err)
	}
}

func TestLoadAcceptsRateLimitsInsideSafeBounds(t *testing.T) {
	setTestEnvironment(t)
	t.Setenv("KOTAE_RATE_LIMIT_PER_MINUTE", "10")
	t.Setenv("KOTAE_RATE_LIMIT_PER_DAY", "100")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimits != (guard.Limits{PerMinute: 10, PerDay: 100}) {
		t.Fatalf("rate limits = %+v", cfg.RateLimits)
	}
}

func TestLoadRejectsUnsafeRateLimitOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "minute malformed", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "many"},
		{name: "minute disabled", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "0"},
		{name: "minute too high", key: "KOTAE_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "day malformed", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "many"},
		{name: "day disabled", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "0"},
		{name: "day too high", key: "KOTAE_RATE_LIMIT_PER_DAY", value: "201"},
		{name: "voice app minute too high", key: "KOTAE_VOICE_APP_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "voice app day disabled", key: "KOTAE_VOICE_APP_RATE_LIMIT_PER_DAY", value: "0"},
		{name: "passkey client minute too high", key: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_MINUTE", value: "21"},
		{name: "passkey client day too high", key: "KOTAE_PASSKEY_CLIENT_RATE_LIMIT_PER_DAY", value: "201"},
		{name: "passkey app breaker minute too high", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_MINUTE", value: "301"},
		{name: "passkey app breaker day too high", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", value: "20001"},
		{name: "passkey app breaker disabled", key: "KOTAE_PASSKEY_APP_CIRCUIT_BREAKER_PER_DAY", value: "0"},
		{name: "request timeout collides with write deadline", key: "KOTAE_REQUEST_TIMEOUT", value: "51s"},
		{name: "voice timeout leaves no speech reserve", key: "KOTAE_VOICE_TIMEOUT", value: "14s"},
		{name: "voice timeout collides with write deadline", key: "KOTAE_VOICE_TIMEOUT", value: "51s"},
		{name: "single utterance speech primary", key: "KOTAE_SPEECH_MODEL", value: "short"},
		{name: "retired speech primary", key: "KOTAE_SPEECH_MODEL", value: "long"},
		{name: "unreviewed alias speech primary", key: "KOTAE_SPEECH_MODEL", value: "latest_long"},
		{name: "unreviewed speech voice", key: "KOTAE_SPEECH_VOICE", value: "ja-JP-Neural2-B"},
		{name: "unreviewed native model", key: "KOTAE_NATIVE_AUDIO_MODEL", value: "gemini-live-latest"},
		{name: "unreviewed native voice", key: "KOTAE_NATIVE_AUDIO_VOICE", value: "Puck"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setTestEnvironment(t)
			t.Setenv(test.key, test.value)

			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; want error")
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Fatalf("error = %q; want environment variable name", err)
			}
		})
	}
}
