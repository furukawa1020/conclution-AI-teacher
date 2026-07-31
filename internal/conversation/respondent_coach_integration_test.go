package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/answercontract"
	"github.com/furukawa1020/conclution-ai-teacher/internal/respondent"
)

func TestAgentNaturalForegroundAnswerNeverBecomesHiddenCoach(t *testing.T) {
	const (
		uid          = "uid-natural-one-shot-coach"
		followUp     = "理由はあとで聞きます。まず、目的は何ですか？"
		answer       = "目的は評価基準をそろえることです"
		naturalReply = "評価基準をそろえたいんですね。今いちばん気になる部分はどこですか？"
	)
	clarify := validModelPlan()
	clarify.InterventionPolicy = "clarify"
	clarify.Intervention.Act = "clarify"
	clarify.SpokenReply = followUp
	clarify.AnswerContract = validCriticContract(followUp)
	natural := validModelPlan()
	natural.InterventionPolicy = "clarify"
	natural.Intervention.Act = "clarify"
	natural.SpokenReply = naturalReply
	natural.AnswerContract = validCriticContract(naturalReply)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, clarify)},
		{body: encodeContract(t, validCriticContract(followUp))},
		{body: encodePlan(t, natural)},
		{body: encodeContract(t, validCriticContract(naturalReply))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "まだ目的を決めきれていません",
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("assistant follow-up: %v", err)
	}
	if first.AssistanceTarget != "assistant" ||
		first.CoachPhase != "none" ||
		first.CoachAction != "none" ||
		first.SpokenReply != followUp {
		t.Fatalf("assistant follow-up metadata: %#v", first)
	}
	armed, err := agent.codec.open(uid, first.StateToken)
	if err != nil {
		t.Fatalf("open armed state: %v", err)
	}
	if !armed.PendingAnswer.Active ||
		!armed.PendingAnswer.AssistantFollowUp ||
		armed.PendingAnswer.Operator != answercontract.OperatorPurpose ||
		armed.PendingAnswer.Subject != assistantFollowUpSubject {
		t.Fatalf("validated follow-up did not arm one-shot frame: %#v", armed.PendingAnswer)
	}
	armedJSON, err := json.Marshal(armed)
	if err != nil {
		t.Fatalf("marshal armed state: %v", err)
	}
	if bytes.Contains(armedJSON, []byte(followUp)) ||
		bytes.Contains(armedJSON, []byte("評価基準の導入目的")) {
		t.Fatalf("assistant question prose entered state: %s", armedJSON)
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     answer,
		StateToken:    first.StateToken,
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("natural foreground answer: %v", err)
	}
	if second.AssistanceTarget != "assistant" ||
		second.RespondentStage != "none" ||
		second.CoachPhase != "none" ||
		second.CoachAction != "none" ||
		second.SpokenReply != naturalReply {
		t.Fatalf("ordinary answer was turned into hidden coaching: %#v", second)
	}
	if len(fake.calls) != 4 ||
		!strings.Contains(fake.calls[2].prompt, `"support_style":"listen"`) {
		t.Fatalf("ordinary answer turn did not suppress stacked optional questions: %#v", fake.calls)
	}
	completed, err := agent.codec.open(uid, second.StateToken)
	if err != nil {
		t.Fatalf("open completed state: %v", err)
	}
	if completed.PendingAnswer.Active {
		t.Fatalf("observation-only frame survived one turn: %#v", completed.PendingAnswer)
	}
	if completed.Support == nil || completed.Support.QuestionCooldown != 1 {
		t.Fatalf("ordinary follow-up did not pause repeated questions: %#v", completed.Support)
	}
}

func TestAgentExplicitRespondentCoachAcceptsLateAnswerWithoutRestatement(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-late-answer"
		questionText = "上司に、導入目的は何かと聞かれました"
		lateAnswer   = "判断のばらつきを減らすためです。目的は評価基準をそろえることです"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
		fixedTip     = "うん、答えは聞こえました。次は今の答えを最初に置くと、もっと伝わりやすいです。そのままで大丈夫です。"
	)
	awaiting := respondentAwaitingPlan()
	late := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		lateAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, late)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			lateAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionLater,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。どう答えればいいですか",
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_answer", "elicit")
	firstState := openCoachState(t, agent, uid, first.StateToken)
	if !firstState.PendingAnswer.Active ||
		firstState.PendingAnswer.Attempts != 0 ||
		firstState.PendingAnswer.Subject != "質問が求める目的" {
		t.Fatalf("initial coach frame: %#v", firstState.PendingAnswer)
	}
	stateJSON, err := json.Marshal(firstState)
	if err != nil {
		t.Fatalf("marshal coach state: %v", err)
	}
	for _, forbidden := range []string{questionText, "導入目的"} {
		if bytes.Contains(stateJSON, []byte(forbidden)) {
			t.Fatalf("question content entered state: %s", stateJSON)
		}
	}

	second, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     lateAnswer,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("A later: %v", err)
	}
	assertCoachMetadata(t, second, "complete", "complete")
	if second.SpokenReply != fixedTip ||
		strings.Contains(second.SpokenReply, proxyDraft) ||
		strings.Contains(second.SpokenReply, lateAnswer) ||
		strings.HasSuffix(second.SpokenReply, "？") {
		t.Fatalf("late answer did not close with a fixed optional tip: %#v", second)
	}
	following := openCoachState(t, agent, uid, second.StateToken)
	if following.PendingAnswer.Active {
		t.Fatalf("accepted late answer retained a follow-up: %#v", following.PendingAnswer)
	}
	if following.Support == nil ||
		following.Support.VerifiedFirstAnswers != 0 ||
		following.Support.QuestionCooldown != questionCooldownAfterPass {
		t.Fatalf("late answer was counted as a verified first answer: %#v", following.Support)
	}
}

func TestAgentExplicitFirstAnswerUpdatesBoundedFadingMetadata(t *testing.T) {
	const (
		uid          = "uid-explicit-coach-first-answer"
		questionText = "上司に、導入目的は何かと聞かれました"
		coreAnswer   = "目的は評価基準をそろえることです。判断のばらつきを減らします"
		proxyDraft   = "AIが本人の代わりに作った回答です。"
		fixedReply   = "なるほど、そう考えているんですね。"
	)
	core := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		coreAnswer,
		"目的は評価基準をそろえることです",
		proxyDraft,
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, respondentAwaitingPlan())},
		{body: encodePlan(t, core)},
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			coreAnswer,
			"目的は評価基準をそろえることです",
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)

	first, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     questionText + "。答え方を一問だけ手伝って",
	})
	if err != nil {
		t.Fatalf("elicit: %v", err)
	}
	assertCoachMetadata(t, first, "awaiting_answer", "elicit")

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     coreAnswer,
		StateToken:    first.StateToken,
	})
	if err != nil {
		t.Fatalf("A first: %v", err)
	}
	assertCoachMetadata(t, result, "complete", "complete")
	if result.SpokenReply != fixedReply ||
		strings.Contains(result.SpokenReply, proxyDraft) ||
		strings.Contains(result.SpokenReply, coreAnswer) ||
		strings.HasSuffix(result.SpokenReply, "？") {
		t.Fatalf("core completion opened another test: %#v", result)
	}
	following := openCoachState(t, agent, uid, result.StateToken)
	if following.PendingAnswer.Active {
		t.Fatalf("successful explicit coaching retained a follow-up: %#v", following.PendingAnswer)
	}
	if following.Support == nil ||
		following.Support.VerifiedFirstAnswers != 1 ||
		following.Support.QuestionCooldown != questionCooldownAfterAnswer {
		t.Fatalf("verified explicit answer did not update bounded fading metadata: %#v", following.Support)
	}
}

func TestAgentPlannerSliceCannotSpoofVerifiedFirst(t *testing.T) {
	const (
		uid        = "uid-coach-whole-utterance-order"
		utterance  = "判断のばらつきを減らしたくて目的は評価基準をそろえることです。"
		extracted  = "目的は評価基準をそろえることです。"
		commitment = "目的は評価基準をそろえることです"
		fixedTip   = "うん、答えは聞こえました。次は今の答えを最初に置くと、もっと伝わりやすいです。そのままで大丈夫です。"
	)
	plan := coachAttemptPlan(
		answercontract.OperatorPurpose,
		answercontract.SlotPurpose,
		"導入目的",
		extracted,
		commitment,
		"draftによる並べ替えです。",
	)
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, plan)},
		// Reproduce a critic that trusts the planner's narrower extraction.
		// Server-side evaluation must still bind it to the complete utterance.
		{body: encodeContract(t, coachCriticContract(
			answercontract.OperatorPurpose,
			answercontract.SlotPurpose,
			extracted,
			commitment,
			answercontract.PositionFirst,
		))},
	}}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(
		uid,
		coachState(answercontract.OperatorPurpose, respondent.CoachPhaseAwaitingAnswer, 0),
	)
	if err != nil {
		t.Fatalf("seal pending state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     utterance,
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertCoachMetadata(t, result, "complete", "complete")
	if result.SpokenReply != fixedTip {
		t.Fatalf("planner slice changed whole-turn order: %#v", result)
	}
	if len(fake.calls) != 2 ||
		!strings.Contains(fake.calls[1].prompt, `"candidate_spoken_reply":"`+utterance+`"`) ||
		!strings.Contains(fake.calls[1].prompt, `"answer_attempt":"`+extracted+`"`) {
		t.Fatalf("critic was not bound to whole utterance: %#v", fake.calls)
	}
	state := openCoachState(t, agent, uid, result.StateToken)
	if state.PendingAnswer.Active ||
		state.Support == nil ||
		state.Support.VerifiedFirstAnswers != 0 ||
		state.Support.QuestionCooldown != questionCooldownAfterPass {
		t.Fatalf("planner slice advanced verified success: %#v", state)
	}
}

func TestAgentPendingCoachDirectQuestionEscapesToAssistant(t *testing.T) {
	const uid = "uid-coach-direct-question-exit"
	awaiting := respondentAwaitingPlan()
	assistant := validModelPlan()
	assistant.SpokenReply = "日本の首都は東京です。"
	fake := &fakeGenerator{generations: []fakeGeneration{
		{body: encodePlan(t, awaiting)},
		{body: encodePlan(t, assistant)},
		{body: encodeContract(t, validCriticContract(assistant.SpokenReply))},
	}}
	agent := newTestAgent(t, fake)
	initial := coachState(answercontract.OperatorPurpose, respondent.CoachPhaseAwaitingAnswer, 0)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "日本の首都はどこですか？",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("direct assistant question: %v", err)
	}
	if result.AssistanceTarget != "assistant" ||
		result.RespondentStage != "none" ||
		result.CoachPhase != "none" ||
		result.CoachAction != "none" ||
		result.SpokenReply != assistant.SpokenReply {
		t.Fatalf("direct question remained trapped in coaching: %#v", result)
	}
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("direct assistant question did not clear coaching frame")
	}
}

func TestAgentPendingCoachHonorsExplicitOptOutWithoutCallingAModel(t *testing.T) {
	for _, test := range []struct {
		name      string
		utterance string
		wantReply string
	}{
		{
			name:      "does not want to talk",
			utterance: "今日はもう話したくない",
			wantReply: "わかりました。今は話さなくて大丈夫です。",
		},
		{
			name:      "wants conversation without correction",
			utterance: "今日は話すだけにしたい",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
		{
			name:      "natural conversation-only wording",
			utterance: "今日はただ話すだけにして",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
		{
			name:      "negated help request",
			utterance: "答え方を手伝ってほしくない",
			wantReply: "わかりました。言い直しは求めません。そのまま話してください。",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const uid = "uid-coach-explicit-opt-out"
			fake := &fakeGenerator{}
			agent := newTestAgent(t, fake)
			token, err := agent.codec.seal(
				uid,
				coachState(
					answercontract.OperatorPurpose,
					respondent.CoachPhaseAwaitingAnswer,
					0,
				),
			)
			if err != nil {
				t.Fatalf("seal pending state: %v", err)
			}

			result, err := agent.Process(context.Background(), uid, VoiceTurn{
				SchemaVersion: SchemaVersion,
				Utterance:     test.utterance,
				StateToken:    token,
			})
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(fake.calls) != 0 ||
				result.Route != "coach-opt-out-local" ||
				result.AssistanceTarget != "assistant" ||
				result.CoachPhase != "none" ||
				result.CoachAction != "none" ||
				result.SpokenReply != test.wantReply {
				t.Fatalf("explicit opt-out was not honored locally: %#v", result)
			}
			next := openCoachState(t, agent, uid, result.StateToken)
			if next.PendingAnswer.Active {
				t.Fatalf("explicit opt-out retained coach state: %#v", next.PendingAnswer)
			}
			if next.Support == nil || !next.Support.CompanionOnly {
				t.Fatalf("explicit opt-out did not persist session companion mode: %#v", next.Support)
			}
		})
	}
}

func TestAgentCompanionModeCanStartWithoutCoachAndResumeExplicitly(t *testing.T) {
	const uid = "uid-session-companion-mode"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)

	paused, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "今日は話すだけ",
	})
	if err != nil {
		t.Fatalf("enter companion mode: %v", err)
	}
	pausedState := openCoachState(t, agent, uid, paused.StateToken)
	if paused.Route != "coach-opt-out-local" ||
		pausedState.Support == nil ||
		!pausedState.Support.CompanionOnly ||
		len(fake.calls) != 0 {
		t.Fatalf("standalone companion choice reached the model: result=%#v state=%#v", paused, pausedState)
	}

	notResumed, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "コーチを再開しないで",
		StateToken:    paused.StateToken,
	})
	if err != nil {
		t.Fatalf("negated resume: %v", err)
	}
	notResumedState := openCoachState(t, agent, uid, notResumed.StateToken)
	if notResumed.Route != "coach-opt-out-local" ||
		notResumedState.Support == nil ||
		!notResumedState.Support.CompanionOnly ||
		len(fake.calls) != 0 {
		t.Fatalf("negated opt-in resumed coaching: result=%#v state=%#v", notResumed, notResumedState)
	}

	resumed, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "コーチを再開して",
		StateToken:    notResumed.StateToken,
	})
	if err != nil {
		t.Fatalf("resume support: %v", err)
	}
	resumedState := openCoachState(t, agent, uid, resumed.StateToken)
	if resumed.Route != "coach-opt-in-local" ||
		resumedState.Support != nil ||
		len(fake.calls) != 0 {
		t.Fatalf("explicit resume did not remain local: result=%#v state=%#v", resumed, resumedState)
	}
}

func TestAgentPassClearsOnlyCurrentQuestion(t *testing.T) {
	const uid = "uid-one-question-pass"
	fake := &fakeGenerator{}
	agent := newTestAgent(t, fake)
	token, err := agent.codec.seal(uid, coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	))
	if err != nil {
		t.Fatalf("seal pending state: %v", err)
	}
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "今回はパス",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	next := openCoachState(t, agent, uid, result.StateToken)
	if next.PendingAnswer.Active ||
		next.Support == nil ||
		next.Support.CompanionOnly ||
		next.Support.QuestionCooldown != questionCooldownAfterPass {
		t.Fatalf("pass changed more than the current question: %#v", next)
	}
}

func TestCoachOptOutPassPhrasesDoNotMatchOrdinaryWords(t *testing.T) {
	for _, utterance := range []string{
		"パスタを食べました", "今はパスタを食べています",
		"パスポートです", "今回はパスポートの話です", "パスワードを忘れました",
		"今日は話さないといけないことがある", "話したくないわけじゃない",
		"今日は話すだけでは足りない", "直さなくていいわけじゃない",
		"話すだけで英語が上達するという話です", "コーチをやめたくない",
	} {
		if _, ok := coachOptOutReply(utterance); ok {
			t.Fatalf("coachOptOutReply(%q) treated an ordinary word as an opt-out", utterance)
		}
		if shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("shouldRecoverOutsideCoach(%q) treated an ordinary word as an opt-out", utterance)
		}
	}

	for _, utterance := range []string{"パス", "えっと、今回はパスします。", "今はパスしたい"} {
		if _, ok := coachOptOutReply(utterance); !ok {
			t.Fatalf("coachOptOutReply(%q) did not honor an explicit opt-out", utterance)
		}
		if !shouldRecoverOutsideCoach(utterance) {
			t.Fatalf("shouldRecoverOutsideCoach(%q) did not recognize an explicit opt-out", utterance)
		}
	}

	for _, utterance := range []string{
		"うまく答えられないので、答え方を手伝って",
		"どう答えればいいですか",
		"上司が目的を聞いてきたから、答え方を一問だけ手伝って",
		"答え方を一問だけ手伝って。目的は評価基準をそろえることです",
		"自分の回答を整えてほしい",
		"私も答え方を手伝ってほしい",
		"私の希望です。答え方を手伝ってほしい",
		"私の依頼で、回答を直して",
		"私はこう言いました。答え方を手伝ってほしい",
		"母はそう言いました。私は自分の回答を整えてほしい",
		"上司に聞かれました。答え方を一問だけ手伝って",
		"could you please help me answer",
	} {
		if !explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) rejected a direct request", utterance)
		}
	}
	for _, utterance := range []string{
		"上司に聞かれたけど答えられなかった",
		"コーチを再開しないで",
		"答え方を手伝ってほしくない",
		"答え方を手伝ってほしいわけじゃない",
		"友達が「答え方を手伝ってほしい」と言っていた",
		"上司は答え方を手伝ってほしいと言った",
		"友達が答え方を手伝ってほしい",
		"母が答え方を手伝ってほしい",
		"母が答え方を手伝って",
		"母も答え方を手伝ってほしい",
		"母の希望は、答え方を手伝ってほしい",
		"母はこう言いました。答え方を手伝ってほしい",
		"母の希望です。答え方を手伝ってほしい",
		"後輩もそう言いました。回答を直してほしい",
		"私の母はこう言いました。答え方を手伝ってほしい",
		"僕の上司が頼みました。回答を直してほしい",
		"私は母がこう言うのを聞きました。答え方を手伝ってほしい",
		"母はこう頼みました。「回答を直して！」",
		"母はこう言いました。「答え方を手伝って。」",
		"「答え方を手伝って！」",
		"母からの依頼です：回答を直して",
		"先生の依頼で、回答を直して",
		"私の母からの指示です：回答を直して",
		"私の依頼ではなく母の依頼です：回答を直して",
		"後輩が回答を直してほしい",
		"my friend said please help me answer",
		"my friend wants me to practice answering",
		"my friend wants you to help me answer",
	} {
		if explicitCoachOptIn(utterance) {
			t.Fatalf("explicitCoachOptIn(%q) treated non-consent as opt-in", utterance)
		}
	}
}

func TestAgentForegroundExplicitRequestCanCreateRespondentScope(t *testing.T) {
	const uid = "uid-foreground-can-create-coach"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "上司に目的を聞かれたけど答えられません。答え方を一問だけ手伝ってください",
		Ambient:       true,
		Foreground:    true,
	})
	if err != nil {
		t.Fatalf("foreground respondent proposal: %v", err)
	}
	if result.AssistanceTarget != "respondent" ||
		result.RespondentStage != "awaiting_answer" ||
		result.CoachPhase != "awaiting_answer" ||
		result.CoachAction != "elicit" ||
		!result.NeedsClarification {
		t.Fatalf("foreground explicit request did not create respondent scope: %#v", result)
	}
	if len(fake.calls) != 1 ||
		!strings.Contains(fake.calls[0].prompt, `"respondent_mode_allowed":true`) {
		t.Fatalf("foreground planner did not receive bounded respondent authority: %#v", fake.calls)
	}
	if !openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("foreground explicit request did not persist a bounded respondent frame")
	}
}

func TestAgentCoachFillersDoNotConsumeGentleRetry(t *testing.T) {
	const uid = "uid-coach-fillers-no-attempt"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		0,
	)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal initial state: %v", err)
	}

	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "えっと……うーん。",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("filler-only turn: %v", err)
	}
	assertCoachMetadata(t, result, "awaiting_answer", "elicit")
	if result.SpokenReply != "" {
		t.Fatalf("filler-only speech triggered another prompt: %q", result.SpokenReply)
	}
	state := openCoachState(t, agent, uid, result.StateToken)
	if state.PendingAnswer.Attempts != 0 {
		t.Fatalf("filler-only speech consumed the gentle retry: %#v", state.PendingAnswer)
	}
}

func TestAgentCoachReleasesAfterBoundedRetries(t *testing.T) {
	const uid = "uid-coach-release-cap"
	fake := &fakeGenerator{generations: []fakeGeneration{{
		body: encodePlan(t, respondentAwaitingPlan()),
	}}}
	agent := newTestAgent(t, fake)
	initial := coachState(
		answercontract.OperatorPurpose,
		respondent.CoachPhaseAwaitingAnswer,
		respondent.MaxCoachAttempts,
	)
	token, err := agent.codec.seal(uid, initial)
	if err != nil {
		t.Fatalf("seal capped state: %v", err)
	}
	result, err := agent.Process(context.Background(), uid, VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "背景の説明だけを続けます",
		StateToken:    token,
	})
	if err != nil {
		t.Fatalf("bounded release: %v", err)
	}
	assertCoachMetadata(t, result, "blocked", "release")
	if openCoachState(t, agent, uid, result.StateToken).PendingAnswer.Active {
		t.Fatal("release retained the coaching frame")
	}
}

func TestBoundedFollowUpClassifierUsesOnlyFinalQuestion(t *testing.T) {
	tests := []struct {
		name string
		text string
		want answercontract.Operator
		ok   bool
	}{
		{
			name: "declarative reason does not override purpose question",
			text: "理由はあとで聞きます。目的は何ですか？",
			want: answercontract.OperatorPurpose,
			ok:   true,
		},
		{
			name: "status precedes generic definition surface",
			text: "現在の状況は何ですか？",
			want: answercontract.OperatorState,
			ok:   true,
		},
		{
			name: "broad current marker is not status",
			text: "今は何をしたいですか？",
			want: answercontract.OperatorOpen,
			ok:   true,
		},
		{
			name: "two questions are not persisted",
			text: "目的は何ですか？理由は何ですか？",
			ok:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := boundedFollowUpOperator(test.text)
			if got != test.want || ok != test.ok {
				t.Fatalf("boundedFollowUpOperator(%q)=(%q,%v), want (%q,%v)", test.text, got, ok, test.want, test.ok)
			}
		})
	}
	if shouldRecoverOutsideCoach("なぜなら、判断のばらつきを減らせるからです") {
		t.Fatal("answer beginning with なぜなら was treated as a direct AI question")
	}
}

func coachAttemptPlan(
	operator answercontract.Operator,
	slot answercontract.RequiredSlot,
	subject string,
	answer string,
	evidence string,
	plannerDraft string,
) modelPlan {
	plan := validModelPlan()
	plan.Domain = "work"
	plan.Intent = "practice"
	plan.AssistanceTarget = "respondent"
	plan.RespondentStage = "restructure"
	plan.AnswerAttempt = answer
	plan.RespondentEvidence = []modelSlotEvidence{{Slot: slot, Span: evidence}}
	plan.LatentQuestion = "本人が質問へ直接答える"
	plan.ArgumentStructure = "direct_answer"
	plan.InterventionPolicy = "coach"
	plan.SpokenReply = plannerDraft
	plan.Intervention = modelArbiter{
		Benefit: 0.9, InterruptionCost: 0.05, Urgency: 0.1,
		Confidence: 0.98, Act: "restructure",
	}
	plan.AnswerContract = respondentDraftContract(
		operator,
		subject,
		[]answercontract.RequiredSlot{slot},
		evidence,
		plannerDraft,
	)
	return plan
}

func coachCriticContract(
	operator answercontract.Operator,
	slot answercontract.RequiredSlot,
	answer string,
	commitment string,
	position answercontract.PositionClass,
) answercontract.Contract {
	issue := answercontract.IssueNone
	if position == answercontract.PositionLater {
		issue = answercontract.IssueBackgroundFirst
	}
	return answercontract.Contract{
		QuestionFrame: answercontract.QuestionFrame{
			Operator:      operator,
			Subject:       "current respondent question",
			RequiredSlots: []answercontract.RequiredSlot{slot},
			Hypotheses: []answercontract.Hypothesis{{
				Interpretation: "evaluate the person's current answer",
				Confidence:     1,
			}},
		},
		CommitmentFront: answercontract.CommitmentFront{
			FirstCommitment: commitment,
			FillsTarget:     true,
			TargetCoverage:  1,
			FilledSlots:     []answercontract.RequiredSlot{slot},
			PositionClass:   position,
			Calibration:     answercontract.CalibrationCommitted,
			Issue:           issue,
		},
		CounterfactualRepair: answercontract.CounterfactualRepair{
			MinimalAnswer:                 commitment,
			ReconstructedAnswer:           answer,
			MeaningPreservationConfidence: 1,
			RepairGain:                    0,
		},
	}
}

func coachState(
	operator answercontract.Operator,
	phase respondent.CoachPhase,
	attempts uint8,
) conversationState {
	target, _ := answercontract.TargetSlot(operator)
	return conversationState{
		Turn: 1,
		Graph: ThoughtStateGraph{
			Goals:          []string{},
			Claims:         []string{},
			Grounds:        []string{},
			Assumptions:    []string{},
			Constraints:    []string{},
			OpenLoops:      []string{},
			Contradictions: []string{},
			Decisions:      []string{},
		},
		PendingAnswer: PendingAnswerFrame{
			Active:        true,
			Operator:      operator,
			Subject:       pendingSubjectForOperator(operator),
			RequiredSlots: []answercontract.RequiredSlot{target},
			ExpansionOperator: answercontract.Operator(respondent.ExpansionOperator(
				respondent.Operator(operator),
			)),
			Phase:    phase,
			Attempts: attempts,
		},
		LastIntervention: ArbiterDecision{Act: "clarify"},
	}
}

func openCoachState(
	t *testing.T,
	agent *vertexAgent,
	uid string,
	token string,
) conversationState {
	t.Helper()
	state, err := agent.codec.open(uid, token)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return state
}

func assertCoachMetadata(
	t *testing.T,
	result VoiceTurnResult,
	phase string,
	action string,
) {
	t.Helper()
	if result.AssistanceTarget != "respondent" ||
		result.CoachPhase != phase ||
		result.CoachAction != action {
		t.Fatalf("coach metadata=(%q,%q,%q), want respondent/%s/%s: %#v",
			result.AssistanceTarget,
			result.CoachPhase,
			result.CoachAction,
			phase,
			action,
			result,
		)
	}
}
