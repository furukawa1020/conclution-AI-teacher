package respondent

import (
	"math"
)

// VerifierProgressPolicyVersion binds persisted posterior mass to the exact
// verifier projection, update rule, utility model, and safety masks below.
// A policy change must change this value so an older posterior fails closed.
const VerifierProgressPolicyVersion uint16 = 1

// LatentAnswerState is a content-free functional state of the current answer.
// It is deliberately not a diagnosis, trait, transcript, or inferred answer.
type LatentAnswerState uint8

const (
	LatentTargetMissing LatentAnswerState = iota
	LatentAvailableUncommitted
	LatentCommittedLate
	LatentCommittedFirst
	LatentVerificationUnknown
	latentAnswerStateCount
)

// VerifierSignal is the finite projection of one verifier's assessment. It
// contains neither the person's words nor an AI reconstruction of them.
type VerifierSignal uint8

const (
	VerifierSignalInvalid VerifierSignal = iota
	VerifierSignalMissing
	VerifierSignalAvailable
	VerifierSignalLater
	VerifierSignalFirst
	VerifierSignalAmbiguous
	VerifierSignalRejected
)

// AnswerEvidence is the joint, finite observation emitted by the two
// independent answer verifiers.
type AnswerEvidence uint8

const (
	AnswerEvidenceVerificationUnknown AnswerEvidence = iota
	AnswerEvidenceHesitation
	AnswerEvidenceTargetMissing
	AnswerEvidenceAvailableUncommitted
	AnswerEvidenceCommittedLate
	AnswerEvidenceCommittedFirst
	AnswerEvidenceAmbiguous
)

// AnswerPhase is a finite copy of CoachPhase. An unknown external value is
// mapped to AnswerPhaseInvalid and fails closed in the action mask.
type AnswerPhase uint8

const (
	AnswerPhaseInvalid AnswerPhase = iota
	AnswerPhaseNone
	AnswerPhaseAwaitingAnswer
	AnswerPhaseAwaitingRestatement
	AnswerPhaseExpanding
	AnswerPhaseComplete
	AnswerPhaseBlocked
)

// AnswerAttemptLevel bounds control state to the existing two-attempt policy.
type AnswerAttemptLevel uint8

const (
	AnswerAttemptNone AnswerAttemptLevel = iota
	AnswerAttemptOne
	AnswerAttemptLimit
)

// AnswerObservation is the complete finite observation used by the belief
// filter. It cannot retain a diagnosis, source text, evidence span, subject,
// or generated meaning.
type AnswerObservation struct {
	Evidence              AnswerEvidence
	GateSignal            VerifierSignal
	CriticSignal          VerifierSignal
	Phase                 AnswerPhase
	Attempts              AnswerAttemptLevel
	OneShot               bool
	Hesitation            bool
	VerificationAvailable bool
}

// VerifierProgressPosterior is a normalized belief over the five functional
// states reported by the independent verifiers. It is not a model of the
// person's memory, intent, diagnosis, or answer-retrieval state.
// The named fields make persistence order-independent and keep accidental new
// states from silently entering the controller.
type VerifierProgressPosterior struct {
	TargetMissing        float64 `json:"target_missing"`
	AvailableUncommitted float64 `json:"available_uncommitted"`
	CommittedLate        float64 `json:"committed_late"`
	CommittedFirst       float64 `json:"committed_first"`
	VerificationUnknown  float64 `json:"verification_unknown"`
}

// AnswerSupportAction is structural control only. None of its values can
// supply, paraphrase, or reconstruct the requested answer.
type AnswerSupportAction string

const (
	AnswerSupportWait        AnswerSupportAction = "wait"
	AnswerSupportElicit      AnswerSupportAction = "elicit"
	AnswerSupportRestate     AnswerSupportAction = "restate"
	AnswerSupportComplete    AnswerSupportAction = "complete"
	AnswerSupportRelease     AnswerSupportAction = "release"
	answerSupportActionCount                     = 5
)

// AnswerReasonCode is numeric so telemetry cannot accidentally contain user
// content. Values are stable and grouped by terminal, prompting, waiting, and
// bounded-release outcomes.
type AnswerReasonCode uint16

const (
	AnswerReasonTerminalComplete         AnswerReasonCode = 1000
	AnswerReasonVerifiedFirst            AnswerReasonCode = 1010
	AnswerReasonLateAccepted             AnswerReasonCode = 1020
	AnswerReasonExpansionAccepted        AnswerReasonCode = 1030
	AnswerReasonTargetMissing            AnswerReasonCode = 2000
	AnswerReasonAvailableUncommitted     AnswerReasonCode = 2010
	AnswerReasonAmbiguous                AnswerReasonCode = 2020
	AnswerReasonLateRestatement          AnswerReasonCode = 2030
	AnswerReasonHesitation               AnswerReasonCode = 3000
	AnswerReasonVerificationUnavailable  AnswerReasonCode = 3010
	AnswerReasonVerificationInconclusive AnswerReasonCode = 3020
	AnswerReasonInvalidPhase             AnswerReasonCode = 3030
	AnswerReasonAttemptLimit             AnswerReasonCode = 4000
	AnswerReasonScopeRejected            AnswerReasonCode = 4010
)

// AnswerActionUtility is the auditable counterfactual utility for one action.
// Positive terms reward an A-first outcome and information gain. The remaining
// terms are costs. LeakageRisk is structurally zero for this non-generative
// controller and remains explicit so a future content-bearing action cannot be
// added without changing the utility contract and tests.
type AnswerActionUtility struct {
	Action           AnswerSupportAction
	Allowed          bool
	AFirstSuccess    float64
	InformationGain  float64
	CognitiveLoad    float64
	InterruptionRisk float64
	CoercionRisk     float64
	LeakageRisk      float64
	Total            float64
}

// AnswerControllerInput is the complete policy boundary. Every field is a
// finite enum, boolean, or fixed-size numeric value. In particular, callers
// cannot provide an Assessment, reconstructed answer, evidence span, string,
// byte slice, map, interface, or pointer. Evidence is derived internally from
// the two verifier signals and can never be supplied by a caller.
type AnswerControllerInput struct {
	Prior                 VerifierProgressPosterior
	GateSignal            VerifierSignal
	CriticSignal          VerifierSignal
	Phase                 AnswerPhase
	Attempts              AnswerAttemptLevel
	OneShot               bool
	Hesitation            bool
	VerificationAvailable bool
}

// AnswerControlDecision is a deterministic, content-free control result.
// NextAttempts never exceeds MaxCoachAttempts. KeepPending and VerifiedFirst
// retain the safety semantics used by GuideAttempt.
type AnswerControlDecision struct {
	Observation   AnswerObservation
	Posterior     VerifierProgressPosterior
	Action        AnswerSupportAction
	ReasonCode    AnswerReasonCode
	Utilities     [answerSupportActionCount]AnswerActionUtility
	NextAttempts  uint8
	KeepPending   bool
	VerifiedFirst bool
	PriorRepaired bool
}

// DefaultVerifierProgressPosterior is the fresh, content-free prior used for
// one verifier-progress scope. It is intentionally separate from QARC's
// retrieval-state prior and is not a population or clinical estimate.
func DefaultVerifierProgressPosterior() VerifierProgressPosterior {
	return VerifierProgressPosterior{
		TargetMissing:        0.30,
		AvailableUncommitted: 0.25,
		CommittedLate:        0.10,
		CommittedFirst:       0.15,
		VerificationUnknown:  0.20,
	}
}

// Probability returns one named posterior probability. Invalid states return
// zero rather than indexing outside the fixed state space.
func (posterior VerifierProgressPosterior) Probability(state LatentAnswerState) float64 {
	values := posteriorValues(posterior)
	if state >= latentAnswerStateCount {
		return 0
	}
	return values[state]
}

// observeAnswerState validates the finite policy input and derives joint
// evidence. It is deliberately unexported so no caller can bypass the boundary
// by constructing evidence directly.
func observeAnswerState(input AnswerControllerInput) AnswerObservation {
	observation := AnswerObservation{
		GateSignal:            normalizeVerifierSignal(input.GateSignal),
		CriticSignal:          normalizeVerifierSignal(input.CriticSignal),
		Phase:                 normalizeAnswerPhase(input.Phase),
		Attempts:              normalizeAttemptLevel(input.Attempts),
		OneShot:               input.OneShot,
		Hesitation:            input.Hesitation,
		VerificationAvailable: input.VerificationAvailable,
	}
	observation.Evidence = combineAnswerEvidence(observation)
	return observation
}

// PlanAnswerSupport observes, updates, evaluates every counterfactual action,
// applies non-negotiable safety masks, and returns the highest-utility allowed
// action. It is a pure function: it reads no clock, random source, model, or
// mutable package state.
func PlanAnswerSupport(input AnswerControllerInput) AnswerControlDecision {
	observation := observeAnswerState(input)
	posterior, repaired := updateVerifierProgressPosterior(input.Prior, observation)
	allowed := allowedAnswerActions(observation)
	utilities := evaluateAnswerActions(posterior, observation, allowed)
	action := selectAnswerAction(utilities)

	nextAttempts := attemptsFromLevel(observation.Attempts)
	if action == AnswerSupportElicit || action == AnswerSupportRestate {
		if nextAttempts < MaxCoachAttempts {
			nextAttempts++
		}
	}
	if action == AnswerSupportRelease {
		nextAttempts = MaxCoachAttempts
	}

	return AnswerControlDecision{
		Observation:   observation,
		Posterior:     posterior,
		Action:        action,
		ReasonCode:    reasonForAnswerAction(observation, action),
		Utilities:     utilities,
		NextAttempts:  nextAttempts,
		KeepPending:   action == AnswerSupportWait || action == AnswerSupportElicit || action == AnswerSupportRestate,
		VerifiedFirst: action == AnswerSupportComplete && observation.Evidence == AnswerEvidenceCommittedFirst && observation.Phase != AnswerPhaseExpanding,
		PriorRepaired: repaired,
	}
}

func combineAnswerEvidence(observation AnswerObservation) AnswerEvidence {
	if observation.Hesitation {
		return AnswerEvidenceHesitation
	}
	if !observation.VerificationAvailable || observation.Phase == AnswerPhaseInvalid {
		return AnswerEvidenceVerificationUnknown
	}
	// Invalid or explicitly rejected evidence must dominate every softer label
	// from the peer verifier. Otherwise an ambiguous peer could turn a
	// fail-closed observation into a prompt-producing state.
	if verifierFailedClosed(observation.GateSignal) ||
		verifierFailedClosed(observation.CriticSignal) {
		return AnswerEvidenceVerificationUnknown
	}
	if observation.GateSignal == VerifierSignalAmbiguous ||
		observation.CriticSignal == VerifierSignalAmbiguous {
		return AnswerEvidenceAmbiguous
	}
	if observation.GateSignal == VerifierSignalFirst &&
		observation.CriticSignal == VerifierSignalFirst {
		return AnswerEvidenceCommittedFirst
	}
	if observation.GateSignal == VerifierSignalLater &&
		observation.CriticSignal == VerifierSignalLater {
		return AnswerEvidenceCommittedLate
	}
	if verifierHasCommittedTarget(observation.GateSignal) ||
		verifierHasCommittedTarget(observation.CriticSignal) {
		return AnswerEvidenceVerificationUnknown
	}
	if observation.GateSignal == VerifierSignalMissing ||
		observation.CriticSignal == VerifierSignalMissing {
		return AnswerEvidenceTargetMissing
	}
	if observation.GateSignal == VerifierSignalAvailable ||
		observation.CriticSignal == VerifierSignalAvailable {
		return AnswerEvidenceAvailableUncommitted
	}
	return AnswerEvidenceVerificationUnknown
}

func verifierFailedClosed(signal VerifierSignal) bool {
	return signal == VerifierSignalInvalid || signal == VerifierSignalRejected
}

func verifierHasCommittedTarget(signal VerifierSignal) bool {
	return signal == VerifierSignalFirst || signal == VerifierSignalLater
}

func verifierHasTarget(signal VerifierSignal) bool {
	return verifierHasCommittedTarget(signal) || signal == VerifierSignalAvailable
}

func normalizeVerifierSignal(signal VerifierSignal) VerifierSignal {
	switch signal {
	case VerifierSignalMissing, VerifierSignalAvailable, VerifierSignalLater,
		VerifierSignalFirst, VerifierSignalAmbiguous, VerifierSignalRejected:
		return signal
	default:
		return VerifierSignalInvalid
	}
}

func normalizeAnswerPhase(phase AnswerPhase) AnswerPhase {
	switch phase {
	case AnswerPhaseNone, AnswerPhaseAwaitingAnswer,
		AnswerPhaseAwaitingRestatement, AnswerPhaseExpanding,
		AnswerPhaseComplete, AnswerPhaseBlocked:
		return phase
	default:
		return AnswerPhaseInvalid
	}
}

func normalizeAttemptLevel(attempts AnswerAttemptLevel) AnswerAttemptLevel {
	switch attempts {
	case AnswerAttemptNone, AnswerAttemptOne, AnswerAttemptLimit:
		return attempts
	default:
		return AnswerAttemptLimit
	}
}

func attemptsFromLevel(level AnswerAttemptLevel) uint8 {
	switch level {
	case AnswerAttemptNone:
		return 0
	case AnswerAttemptOne:
		return 1
	default:
		return MaxCoachAttempts
	}
}

func posteriorValues(posterior VerifierProgressPosterior) [latentAnswerStateCount]float64 {
	return [latentAnswerStateCount]float64{
		posterior.TargetMissing,
		posterior.AvailableUncommitted,
		posterior.CommittedLate,
		posterior.CommittedFirst,
		posterior.VerificationUnknown,
	}
}

func posteriorFromValues(values [latentAnswerStateCount]float64) VerifierProgressPosterior {
	return VerifierProgressPosterior{
		TargetMissing:        values[LatentTargetMissing],
		AvailableUncommitted: values[LatentAvailableUncommitted],
		CommittedLate:        values[LatentCommittedLate],
		CommittedFirst:       values[LatentCommittedFirst],
		VerificationUnknown:  values[LatentVerificationUnknown],
	}
}

func updateVerifierProgressPosterior(
	prior VerifierProgressPosterior,
	observation AnswerObservation,
) (VerifierProgressPosterior, bool) {
	values := posteriorValues(prior)
	values, repaired := normalizeProbabilityValues(values)
	defaults := posteriorValues(DefaultVerifierProgressPosterior())

	// This prediction step is a fixed transition prior. Functional answer
	// state can change between turns, and no earlier zero can be permanent.
	for index := range values {
		values[index] = values[index]*0.97 + defaults[index]*0.03
	}
	likelihood := evidenceLikelihood(observation.Evidence)
	for index := range values {
		values[index] *= likelihood[index]
	}
	values, _ = normalizeProbabilityValues(values)
	return posteriorFromValues(values), repaired
}

func normalizeProbabilityValues(
	values [latentAnswerStateCount]float64,
) ([latentAnswerStateCount]float64, bool) {
	repaired := false
	sum := 0.0
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			values[index] = 0
			repaired = true
			continue
		}
		if value > 1 {
			repaired = true
		}
		sum += values[index]
	}
	if !(sum > 0) || math.IsNaN(sum) || math.IsInf(sum, 0) {
		return posteriorValues(DefaultVerifierProgressPosterior()), true
	}
	if math.Abs(sum-1) > 1e-12 {
		repaired = true
	}
	for index := range values {
		values[index] /= sum
	}

	// Assign the floating-point residual to the last state so repeated updates
	// remain normalized and deterministic in the fixed state order.
	partial := 0.0
	for index := 0; index < int(latentAnswerStateCount)-1; index++ {
		partial += values[index]
	}
	values[LatentVerificationUnknown] = math.Max(0, 1-partial)
	return values, repaired
}

func evidenceLikelihood(evidence AnswerEvidence) [latentAnswerStateCount]float64 {
	switch evidence {
	case AnswerEvidenceTargetMissing:
		return [latentAnswerStateCount]float64{930, 30, 2, 1, 37}
	case AnswerEvidenceAvailableUncommitted:
		return [latentAnswerStateCount]float64{20, 900, 20, 5, 55}
	case AnswerEvidenceCommittedLate:
		return [latentAnswerStateCount]float64{1, 4, 980, 5, 10}
	case AnswerEvidenceCommittedFirst:
		return [latentAnswerStateCount]float64{1, 3, 8, 980, 8}
	case AnswerEvidenceAmbiguous:
		return [latentAnswerStateCount]float64{80, 100, 50, 20, 750}
	case AnswerEvidenceHesitation:
		return [latentAnswerStateCount]float64{1, 1, 1, 1, 1}
	case AnswerEvidenceVerificationUnknown:
		return [latentAnswerStateCount]float64{5, 8, 8, 5, 974}
	default:
		return [latentAnswerStateCount]float64{5, 8, 8, 5, 974}
	}
}

func allowedAnswerActions(observation AnswerObservation) [answerSupportActionCount]bool {
	var allowed [answerSupportActionCount]bool
	allow := func(action AnswerSupportAction) {
		allowed[actionIndex(action)] = true
	}

	// The deterministic continuity gate is authoritative for question scope.
	// A rejected scope must be released instead of waiting and accidentally
	// carrying an old answer contract into a new topic.
	if observation.GateSignal == VerifierSignalRejected {
		allow(AnswerSupportRelease)
		return allowed
	}
	if observation.Phase == AnswerPhaseComplete {
		if observation.Evidence == AnswerEvidenceCommittedFirst ||
			observation.Evidence == AnswerEvidenceCommittedLate {
			allow(AnswerSupportComplete)
		} else {
			allow(AnswerSupportWait)
		}
		return allowed
	}
	if observation.Hesitation ||
		observation.Evidence == AnswerEvidenceHesitation ||
		!observation.VerificationAvailable {
		allow(AnswerSupportWait)
		return allowed
	}
	if observation.Phase == AnswerPhaseInvalid ||
		observation.Evidence == AnswerEvidenceVerificationUnknown {
		allow(AnswerSupportWait)
		return allowed
	}
	if observation.Evidence == AnswerEvidenceCommittedFirst {
		allow(AnswerSupportComplete)
		return allowed
	}
	if observation.Phase == AnswerPhaseExpanding &&
		verifierHasTarget(observation.GateSignal) &&
		verifierHasTarget(observation.CriticSignal) {
		// Optional expansion is never another A-first test.
		allow(AnswerSupportComplete)
		return allowed
	}
	if observation.Evidence == AnswerEvidenceCommittedLate {
		if !observation.OneShot &&
			observation.Phase != AnswerPhaseExpanding {
			if observation.Attempts >= AnswerAttemptOne {
				allow(AnswerSupportRelease)
				return allowed
			}
			allow(AnswerSupportWait)
			allow(AnswerSupportRestate)
			return allowed
		}
		allow(AnswerSupportComplete)
		return allowed
	}
	if observation.Attempts >= AnswerAttemptOne {
		allow(AnswerSupportRelease)
		return allowed
	}

	// Missing, structurally available-but-uncommitted, and ambiguous evidence
	// permit either more space or exactly one bounded structural elicitation.
	allow(AnswerSupportWait)
	allow(AnswerSupportElicit)
	return allowed
}

func evaluateAnswerActions(
	posterior VerifierProgressPosterior,
	observation AnswerObservation,
	allowed [answerSupportActionCount]bool,
) [answerSupportActionCount]AnswerActionUtility {
	actions := [answerSupportActionCount]AnswerSupportAction{
		AnswerSupportWait,
		AnswerSupportElicit,
		AnswerSupportRestate,
		AnswerSupportComplete,
		AnswerSupportRelease,
	}
	values := posteriorValues(posterior)
	var utilities [answerSupportActionCount]AnswerActionUtility
	for actionPosition, action := range actions {
		utility := AnswerActionUtility{
			Action:  action,
			Allowed: allowed[actionPosition],
		}
		for state := LatentAnswerState(0); state < latentAnswerStateCount; state++ {
			probability := values[state]
			effect := counterfactualEffect(action, state, observation.Attempts)
			utility.AFirstSuccess += probability * effect.aFirstSuccess
			utility.CognitiveLoad += probability * effect.cognitiveLoad
			utility.InterruptionRisk += probability * effect.interruptionRisk
			utility.CoercionRisk += probability * effect.coercionRisk
			utility.LeakageRisk += probability * effect.leakageRisk
		}
		utility.InformationGain = expectedAnswerInformationGain(values, action)
		utility.Total = 2.40*utility.AFirstSuccess +
			0.80*utility.InformationGain -
			0.55*utility.CognitiveLoad -
			0.65*utility.InterruptionRisk -
			0.90*utility.CoercionRisk -
			1.40*utility.LeakageRisk
		utilities[actionPosition] = utility
	}
	return utilities
}

type answerCounterfactualEffect struct {
	aFirstSuccess    float64
	cognitiveLoad    float64
	interruptionRisk float64
	coercionRisk     float64
	leakageRisk      float64
}

func counterfactualEffect(
	action AnswerSupportAction,
	state LatentAnswerState,
	attempts AnswerAttemptLevel,
) answerCounterfactualEffect {
	repetitionCost := 0.0
	if attempts >= AnswerAttemptOne &&
		(action == AnswerSupportElicit || action == AnswerSupportRestate) {
		repetitionCost = 0.20
	}

	switch action {
	case AnswerSupportWait:
		success := [latentAnswerStateCount]float64{0.04, 0.20, 0.18, 0.90, 0.02}
		interrupt := [latentAnswerStateCount]float64{0.01, 0.01, 0.03, 0.08, 0.01}
		return answerCounterfactualEffect{
			aFirstSuccess: success[state], cognitiveLoad: 0.02,
			interruptionRisk: interrupt[state], coercionRisk: 0,
		}
	case AnswerSupportElicit:
		success := [latentAnswerStateCount]float64{0.25, 0.58, 0.28, 0.92, 0.10}
		load := [latentAnswerStateCount]float64{0.28, 0.22, 0.30, 0.34, 0.24}
		interrupt := [latentAnswerStateCount]float64{0.08, 0.12, 0.18, 0.45, 0.12}
		return answerCounterfactualEffect{
			aFirstSuccess: success[state], cognitiveLoad: load[state],
			interruptionRisk: interrupt[state], coercionRisk: 0.10 + repetitionCost,
		}
	case AnswerSupportRestate:
		success := [latentAnswerStateCount]float64{0.10, 0.56, 0.72, 0.95, 0.06}
		load := [latentAnswerStateCount]float64{0.42, 0.40, 0.38, 0.46, 0.42}
		interrupt := [latentAnswerStateCount]float64{0.14, 0.18, 0.12, 0.48, 0.16}
		return answerCounterfactualEffect{
			aFirstSuccess: success[state], cognitiveLoad: load[state],
			interruptionRisk: interrupt[state], coercionRisk: 0.26 + repetitionCost,
		}
	case AnswerSupportComplete:
		success := [latentAnswerStateCount]float64{0, 0, 0, 1, 0}
		return answerCounterfactualEffect{
			aFirstSuccess: success[state], cognitiveLoad: 0.01,
		}
	case AnswerSupportRelease:
		return answerCounterfactualEffect{}
	default:
		return answerCounterfactualEffect{leakageRisk: 1}
	}
}

// expectedAnswerInformationGain is I(latent state; next finite resolution
// signal) in bits under a fixed Bernoulli observation model for each action.
func expectedAnswerInformationGain(
	posterior [latentAnswerStateCount]float64,
	action AnswerSupportAction,
) float64 {
	if action == AnswerSupportComplete || action == AnswerSupportRelease {
		return 0
	}
	resolved := [latentAnswerStateCount]float64{}
	switch action {
	case AnswerSupportWait:
		resolved = [latentAnswerStateCount]float64{0.10, 0.28, 0.55, 0.95, 0.10}
	case AnswerSupportElicit:
		resolved = [latentAnswerStateCount]float64{0.42, 0.68, 0.72, 0.95, 0.30}
	case AnswerSupportRestate:
		resolved = [latentAnswerStateCount]float64{0.18, 0.62, 0.82, 0.96, 0.18}
	default:
		return 0
	}
	probabilityResolved := 0.0
	conditionalEntropy := 0.0
	for state := LatentAnswerState(0); state < latentAnswerStateCount; state++ {
		probabilityResolved += posterior[state] * resolved[state]
		conditionalEntropy += posterior[state] * binaryEntropy(resolved[state])
	}
	informationGain := binaryEntropy(probabilityResolved) - conditionalEntropy
	if informationGain < 0 && informationGain > -1e-12 {
		return 0
	}
	return informationGain
}

func binaryEntropy(probability float64) float64 {
	if probability <= 0 || probability >= 1 {
		return 0
	}
	return -probability*math.Log2(probability) -
		(1-probability)*math.Log2(1-probability)
}

func actionIndex(action AnswerSupportAction) int {
	switch action {
	case AnswerSupportWait:
		return 0
	case AnswerSupportElicit:
		return 1
	case AnswerSupportRestate:
		return 2
	case AnswerSupportComplete:
		return 3
	case AnswerSupportRelease:
		return 4
	default:
		return 0
	}
}

func selectAnswerAction(
	utilities [answerSupportActionCount]AnswerActionUtility,
) AnswerSupportAction {
	selected := AnswerSupportWait
	selectedTotal := -math.MaxFloat64
	found := false
	for _, utility := range utilities {
		if !utility.Allowed {
			continue
		}
		if !found || utility.Total > selectedTotal {
			selected = utility.Action
			selectedTotal = utility.Total
			found = true
		}
	}
	return selected
}

func reasonForAnswerAction(
	observation AnswerObservation,
	action AnswerSupportAction,
) AnswerReasonCode {
	if action == AnswerSupportRelease {
		if observation.GateSignal == VerifierSignalRejected {
			return AnswerReasonScopeRejected
		}
		return AnswerReasonAttemptLimit
	}
	if observation.Phase == AnswerPhaseInvalid {
		return AnswerReasonInvalidPhase
	}
	if observation.Phase == AnswerPhaseComplete && action == AnswerSupportComplete {
		return AnswerReasonTerminalComplete
	}
	if observation.Hesitation || observation.Evidence == AnswerEvidenceHesitation {
		return AnswerReasonHesitation
	}
	if !observation.VerificationAvailable {
		return AnswerReasonVerificationUnavailable
	}
	if action == AnswerSupportComplete && observation.Phase == AnswerPhaseExpanding {
		return AnswerReasonExpansionAccepted
	}
	switch observation.Evidence {
	case AnswerEvidenceCommittedFirst:
		return AnswerReasonVerifiedFirst
	case AnswerEvidenceCommittedLate:
		if action == AnswerSupportRestate {
			return AnswerReasonLateRestatement
		}
		return AnswerReasonLateAccepted
	case AnswerEvidenceTargetMissing:
		return AnswerReasonTargetMissing
	case AnswerEvidenceAvailableUncommitted:
		return AnswerReasonAvailableUncommitted
	case AnswerEvidenceAmbiguous:
		return AnswerReasonAmbiguous
	default:
		return AnswerReasonVerificationInconclusive
	}
}
