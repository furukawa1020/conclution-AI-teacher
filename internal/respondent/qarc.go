package respondent

import (
	"math"
	"sort"
)

// QARCPolicyVersion identifies the transient question-bound retrieval policy.
// It is numeric so a certificate cannot become a prose channel.
const QARCPolicyVersion uint16 = 1

// RetrievalState is a transient functional conversation state, not a
// diagnosis or persistent user trait.
type RetrievalState uint8

const (
	RetrievalTargetLost RetrievalState = iota
	RetrievalSearchBlocked
	RetrievalFormulation
	RetrievalInitiation
	RetrievalSelfMonitoring
	RetrievalReady
	RetrievalKnowledgeUnknown
	RetrievalQuestionUnclear
	retrievalStateCount
)

var qarcStates = [...]RetrievalState{
	RetrievalTargetLost,
	RetrievalSearchBlocked,
	RetrievalFormulation,
	RetrievalInitiation,
	RetrievalSelfMonitoring,
	RetrievalReady,
	RetrievalKnowledgeUnknown,
	RetrievalQuestionUnclear,
}

// QARCAction is a closed, answer-free action language.
type QARCAction uint8

const (
	QARCWait QARCAction = iota
	QARCOperatorCue
	QARCNeutralCue
	QARCRelease
	qarcActionCount
)

var qarcActions = [...]QARCAction{
	QARCWait,
	QARCOperatorCue,
	QARCNeutralCue,
	QARCRelease,
}

// QARCOperator is the finite projection of Operator accepted at the QARC
// boundary. In particular, QARCObservation cannot carry an arbitrary string.
type QARCOperator uint8

const (
	QARCOperatorInvalid QARCOperator = iota
	QARCOperatorBoolean
	QARCOperatorChoice
	QARCOperatorQuantity
	QARCOperatorState
	QARCOperatorCause
	QARCOperatorPurpose
	QARCOperatorProcedure
	QARCOperatorDefinition
	QARCOperatorComparison
	QARCOperatorEvidence
	QARCOperatorOpen
	qarcOperatorCount
)

// ProjectQARCOperator is the only conversion from the wider respondent
// operator representation into QARC's finite control alphabet.
func ProjectQARCOperator(operator Operator) (QARCOperator, bool) {
	switch operator {
	case OperatorBoolean:
		return QARCOperatorBoolean, true
	case OperatorChoice:
		return QARCOperatorChoice, true
	case OperatorQuantity:
		return QARCOperatorQuantity, true
	case OperatorState:
		return QARCOperatorState, true
	case OperatorCause:
		return QARCOperatorCause, true
	case OperatorPurpose:
		return QARCOperatorPurpose, true
	case OperatorProcedure:
		return QARCOperatorProcedure, true
	case OperatorDefinition:
		return QARCOperatorDefinition, true
	case OperatorComparison:
		return QARCOperatorComparison, true
	case OperatorEvidence:
		return QARCOperatorEvidence, true
	case OperatorOpen:
		return QARCOperatorOpen, true
	default:
		return QARCOperatorInvalid, false
	}
}

func (operator QARCOperator) respondentOperator() (Operator, bool) {
	switch operator {
	case QARCOperatorBoolean:
		return OperatorBoolean, true
	case QARCOperatorChoice:
		return OperatorChoice, true
	case QARCOperatorQuantity:
		return OperatorQuantity, true
	case QARCOperatorState:
		return OperatorState, true
	case QARCOperatorCause:
		return OperatorCause, true
	case QARCOperatorPurpose:
		return OperatorPurpose, true
	case QARCOperatorProcedure:
		return OperatorProcedure, true
	case QARCOperatorDefinition:
		return OperatorDefinition, true
	case QARCOperatorComparison:
		return OperatorComparison, true
	case QARCOperatorEvidence:
		return OperatorEvidence, true
	case QARCOperatorOpen:
		return OperatorOpen, true
	default:
		return "", false
	}
}

// QARCTemplateID selects one audited, immutable renderer entry. The QARC
// policy returns this ID rather than speech or prose.
type QARCTemplateID uint8

const (
	QARCTemplateNone QARCTemplateID = iota
	QARCTemplateBoolean
	QARCTemplateChoice
	QARCTemplateQuantity
	QARCTemplateState
	QARCTemplateCause
	QARCTemplatePurpose
	QARCTemplateProcedure
	QARCTemplateDefinition
	QARCTemplateComparison
	QARCTemplateEvidence
	QARCTemplateOpen
	QARCTemplateNeutral
	QARCTemplateRelease
	qarcTemplateCount
)

// QARCCueSlot is the finite answer shape used by the operator template.
type QARCCueSlot uint8

const (
	QARCSlotNone QARCCueSlot = iota
	QARCSlotPolarity
	QARCSlotSelection
	QARCSlotQuantity
	QARCSlotState
	QARCSlotCause
	QARCSlotPurpose
	QARCSlotProcedure
	QARCSlotDefinition
	QARCSlotComparison
	QARCSlotEvidence
	QARCSlotPosition
	qarcSlotCount
)

// QARCReason is a closed explanation code.
type QARCReason uint8

const (
	QARCReasonInvalidObservation QARCReason = iota
	QARCReasonUnboundScope
	QARCReasonHesitationSpace
	QARCReasonFloorNotClear
	QARCReasonAttemptBudgetExhausted
	QARCReasonMinimaxRegret
	QARCReasonOperatorUncertain
	qarcReasonCount
)

// ProbabilityInterval is one coordinate of the credal belief set.
type ProbabilityInterval struct {
	Lower float64
	Upper float64
}

// QARCBelief has a fixed state order and cannot carry arbitrary map keys.
type QARCBelief [retrievalStateCount]ProbabilityInterval

// Interval safely returns one state interval.
func (belief QARCBelief) Interval(state RetrievalState) (ProbabilityInterval, bool) {
	if state >= retrievalStateCount {
		return ProbabilityInterval{}, false
	}
	return belief[state], true
}

// QARCObservation contains interaction structure only. ScopeBound means the
// caller has already bound a unique, current question scope; QARC does not
// infer or accept that scope's answer-bearing content.
type QARCObservation struct {
	Operator              QARCOperator
	ScopeBound            bool
	OperatorConfidence    float64
	EndpointCommitted     bool
	CommitProtected       bool
	NewQuestionScope      bool
	HasSubstantiveAttempt bool
	HesitationOnly        bool
	UserOnsetProbability  float64
	IncidentalProbability float64
	Attempts              uint8
	AudioCancelable       bool
}

// QARCCertificate is finite, numeric, and content-free.
type QARCCertificate struct {
	PolicyVersion       uint16
	Action              QARCAction
	TemplateID          QARCTemplateID
	Slot                QARCCueSlot
	WorstCaseUtility    float64
	WorstCaseRegret     float64
	WaitBestCaseUtility float64
	DominatesWait       bool
	NonInterference     bool
	FloorProtected      bool
	Reason              QARCReason
}

// QARCDecision contains no cue, prose, string field, or string-keyed map.
type QARCDecision struct {
	Action      QARCAction
	TemplateID  QARCTemplateID
	Slot        QARCCueSlot
	Belief      QARCBelief
	Certificate QARCCertificate
}

// DecideQARC preserves the transient credal minimax policy while applying
// non-negotiable input and floor guards before any non-WAIT action.
func DecideQARC(observation QARCObservation) QARCDecision {
	if !validQARCObservation(observation) {
		return qarcInvalidDecision()
	}
	belief := qarcBelief(observation)

	// Invalid or missing control context always wins over the attempt budget.
	// A budget must never turn an unbound or floor-unsafe observation into
	// speech.
	if !observation.ScopeBound {
		return qarcGuardedDecision(
			QARCWait,
			[]QARCAction{QARCWait},
			belief,
			QARCReasonUnboundScope,
			observation,
		)
	}
	if observation.HesitationOnly {
		return qarcGuardedDecision(
			QARCWait,
			[]QARCAction{QARCWait},
			belief,
			QARCReasonHesitationSpace,
			observation,
		)
	}
	if !qarcSpeechAllowed(observation) {
		return qarcGuardedDecision(
			QARCWait,
			[]QARCAction{QARCWait},
			belief,
			QARCReasonFloorNotClear,
			observation,
		)
	}
	if observation.Attempts >= MaxCoachAttempts {
		return qarcGuardedDecision(
			QARCRelease,
			[]QARCAction{QARCRelease},
			belief,
			QARCReasonAttemptBudgetExhausted,
			observation,
		)
	}

	safe := []QARCAction{QARCWait, QARCNeutralCue}
	if _, ok := qarcOperatorSlot(observation.Operator); ok &&
		observation.OperatorConfidence >= 0.65 {
		safe = append(safe, QARCOperatorCue)
	}

	bestAction := QARCWait
	bestRegret := math.Inf(1)
	bestWorst := math.Inf(-1)
	for _, action := range safe {
		regret := qarcWorstCaseRegret(action, safe, belief, observation)
		worst := qarcExpectationBound(
			belief,
			qarcUtilityVector(action, observation),
			false,
		)
		if regret < bestRegret-1e-9 ||
			(math.Abs(regret-bestRegret) <= 1e-9 &&
				(worst > bestWorst+1e-9 ||
					(math.Abs(worst-bestWorst) <= 1e-9 &&
						qarcActionPriority(action) < qarcActionPriority(bestAction)))) {
			bestAction = action
			bestRegret = regret
			bestWorst = worst
		}
	}

	reason := QARCReasonMinimaxRegret
	if bestAction == QARCNeutralCue {
		reason = QARCReasonOperatorUncertain
	}
	return qarcGuardedDecision(bestAction, safe, belief, reason, observation)
}

func validQARCObservation(observation QARCObservation) bool {
	if !validQARCProbability(observation.OperatorConfidence) ||
		!validQARCProbability(observation.UserOnsetProbability) ||
		!validQARCProbability(observation.IncidentalProbability) {
		return false
	}
	_, ok := qarcOperatorSlot(observation.Operator)
	return ok
}

func validQARCProbability(value float64) bool {
	return !math.IsNaN(value) &&
		!math.IsInf(value, 0) &&
		value >= 0 &&
		value <= 1
}

func qarcInvalidDecision() QARCDecision {
	observation := QARCObservation{
		Operator:              QARCOperatorOpen,
		OperatorConfidence:    0,
		UserOnsetProbability:  1,
		IncidentalProbability: 1,
		AudioCancelable:       false,
	}
	return qarcGuardedDecision(
		QARCWait,
		[]QARCAction{QARCWait},
		qarcUnknownBelief(),
		QARCReasonInvalidObservation,
		observation,
	)
}

func qarcUnknownBelief() QARCBelief {
	var belief QARCBelief
	for _, state := range qarcStates {
		belief[state] = ProbabilityInterval{Lower: 0, Upper: 1}
	}
	return belief
}

func qarcBelief(observation QARCObservation) QARCBelief {
	weights := [retrievalStateCount]float64{
		RetrievalTargetLost:       0.20,
		RetrievalSearchBlocked:    0.18,
		RetrievalFormulation:      0.14,
		RetrievalInitiation:       0.14,
		RetrievalSelfMonitoring:   0.10,
		RetrievalReady:            0.14,
		RetrievalKnowledgeUnknown: 0.06,
		RetrievalQuestionUnclear:  0.04,
	}
	if observation.NewQuestionScope {
		weights[RetrievalTargetLost] += 0.12
		weights[RetrievalSearchBlocked] += 0.08
		weights[RetrievalInitiation] += 0.06
	}
	if observation.HasSubstantiveAttempt {
		weights[RetrievalFormulation] += 0.18
		weights[RetrievalReady] += 0.08
		weights[RetrievalTargetLost] -= 0.06
	}
	if observation.HesitationOnly {
		weights[RetrievalReady] += 0.36
		weights[RetrievalSelfMonitoring] += 0.08
	}
	if observation.Attempts > 0 {
		shift := 0.06 * float64(observation.Attempts)
		weights[RetrievalKnowledgeUnknown] += shift
		weights[RetrievalSelfMonitoring] += shift
	}
	unclear := (1 - observation.OperatorConfidence) * 0.45
	weights[RetrievalQuestionUnclear] += unclear
	weights[RetrievalTargetLost] -= unclear * 0.25

	total := 0.0
	for _, state := range qarcStates {
		if weights[state] < 0.001 {
			weights[state] = 0.001
		}
		total += weights[state]
	}
	uncertainty := 0.035 + (1-observation.OperatorConfidence)*0.12
	if observation.HesitationOnly {
		uncertainty += 0.025
	}
	var belief QARCBelief
	for _, state := range qarcStates {
		point := weights[state] / total
		belief[state] = ProbabilityInterval{
			Lower: math.Max(0, point-uncertainty),
			Upper: math.Min(1, point+uncertainty),
		}
	}
	return belief
}

type qarcUtility [retrievalStateCount]float64

func qarcUtilityVector(
	action QARCAction,
	observation QARCObservation,
) qarcUtility {
	var values qarcUtility
	switch action {
	case QARCOperatorCue:
		values = qarcUtility{
			RetrievalTargetLost:       0.95,
			RetrievalSearchBlocked:    0.68,
			RetrievalFormulation:      0.48,
			RetrievalInitiation:       0.58,
			RetrievalSelfMonitoring:   -0.28,
			RetrievalReady:            -0.78,
			RetrievalKnowledgeUnknown: -0.42,
			RetrievalQuestionUnclear:  -0.86,
		}
	case QARCNeutralCue:
		values = qarcUtility{
			RetrievalTargetLost:       0.34,
			RetrievalSearchBlocked:    0.36,
			RetrievalFormulation:      0.28,
			RetrievalInitiation:       0.42,
			RetrievalSelfMonitoring:   -0.10,
			RetrievalReady:            -0.34,
			RetrievalKnowledgeUnknown: 0.04,
			RetrievalQuestionUnclear:  0.18,
		}
	case QARCRelease:
		values = qarcUtility{
			RetrievalTargetLost:       -0.35,
			RetrievalSearchBlocked:    -0.25,
			RetrievalFormulation:      -0.12,
			RetrievalInitiation:       0.12,
			RetrievalSelfMonitoring:   0.62,
			RetrievalReady:            0.18,
			RetrievalKnowledgeUnknown: 0.72,
			RetrievalQuestionUnclear:  0.48,
		}
	default:
		values = qarcUtility{
			RetrievalTargetLost:       -0.36,
			RetrievalSearchBlocked:    -0.10,
			RetrievalFormulation:      0.18,
			RetrievalInitiation:       -0.18,
			RetrievalSelfMonitoring:   0.24,
			RetrievalReady:            0.72,
			RetrievalKnowledgeUnknown: 0.32,
			RetrievalQuestionUnclear:  0.28,
		}
	}

	cost := 0.0
	switch action {
	case QARCOperatorCue:
		cost = 0.05 + 0.50*observation.UserOnsetProbability
	case QARCNeutralCue:
		cost = 0.035 + 0.34*observation.UserOnsetProbability
	case QARCRelease:
		if observation.Attempts < MaxCoachAttempts {
			cost = 0.35
		}
	}
	for _, state := range qarcStates {
		values[state] -= cost
	}
	return values
}

func qarcWorstCaseRegret(
	action QARCAction,
	safe []QARCAction,
	belief QARCBelief,
	observation QARCObservation,
) float64 {
	chosen := qarcUtilityVector(action, observation)
	worstRegret := 0.0
	for _, alternative := range safe {
		other := qarcUtilityVector(alternative, observation)
		var difference qarcUtility
		for _, state := range qarcStates {
			difference[state] = other[state] - chosen[state]
		}
		regret := qarcExpectationBound(belief, difference, true)
		if regret > worstRegret {
			worstRegret = regret
		}
	}
	return qarcRound(worstRegret)
}

type qarcAllocation struct {
	state RetrievalState
	value float64
}

// qarcExpectationBound solves the interval-simplex linear program exactly.
func qarcExpectationBound(
	belief QARCBelief,
	utilities qarcUtility,
	maximum bool,
) float64 {
	allocations := make([]qarcAllocation, 0, len(qarcStates))
	mass := 1.0
	value := 0.0
	for _, state := range qarcStates {
		interval := belief[state]
		value += interval.Lower * utilities[state]
		mass -= interval.Lower
		allocations = append(allocations, qarcAllocation{
			state: state,
			value: utilities[state],
		})
	}
	sort.SliceStable(allocations, func(i, j int) bool {
		if maximum {
			return allocations[i].value > allocations[j].value
		}
		return allocations[i].value < allocations[j].value
	})
	for _, allocation := range allocations {
		if mass <= 1e-12 {
			break
		}
		interval := belief[allocation.state]
		capacity := interval.Upper - interval.Lower
		assigned := math.Min(mass, capacity)
		value += assigned * allocation.value
		mass -= assigned
	}
	if mass > 1e-9 {
		return math.Inf(-1)
	}
	return qarcRound(value)
}

func qarcGuardedDecision(
	action QARCAction,
	admissible []QARCAction,
	belief QARCBelief,
	reason QARCReason,
	observation QARCObservation,
) QARCDecision {
	if !qarcActionInSet(action, admissible) {
		action = QARCWait
		admissible = []QARCAction{QARCWait}
		reason = QARCReasonInvalidObservation
	}
	if action != QARCWait && !qarcSpeechAllowed(observation) {
		action = QARCWait
		admissible = []QARCAction{QARCWait}
		reason = QARCReasonFloorNotClear
	}

	templateID := QARCTemplateNone
	slot := QARCSlotNone
	switch action {
	case QARCOperatorCue:
		var ok bool
		templateID, slot, ok = qarcOperatorTemplate(observation.Operator)
		if !ok {
			action = QARCWait
			admissible = []QARCAction{QARCWait}
			reason = QARCReasonInvalidObservation
			templateID = QARCTemplateNone
			slot = QARCSlotNone
		}
	case QARCNeutralCue:
		templateID = QARCTemplateNeutral
	case QARCRelease:
		templateID = QARCTemplateRelease
	case QARCWait:
		// WAIT deliberately renders no audio.
	default:
		action = QARCWait
		admissible = []QARCAction{QARCWait}
		reason = QARCReasonInvalidObservation
	}

	utility := qarcUtilityVector(action, observation)
	worst := qarcExpectationBound(belief, utility, false)
	waitBest := qarcExpectationBound(
		belief,
		qarcUtilityVector(QARCWait, observation),
		true,
	)
	regret := qarcWorstCaseRegret(
		action,
		admissible,
		belief,
		observation,
	)
	nonInterference := qarcTemplateIsCompiled(action, templateID, slot)
	return QARCDecision{
		Action:     action,
		TemplateID: templateID,
		Slot:       slot,
		Belief:     belief,
		Certificate: QARCCertificate{
			PolicyVersion:       QARCPolicyVersion,
			Action:              action,
			TemplateID:          templateID,
			Slot:                slot,
			WorstCaseUtility:    worst,
			WorstCaseRegret:     regret,
			WaitBestCaseUtility: waitBest,
			DominatesWait:       worst > waitBest,
			NonInterference:     nonInterference,
			FloorProtected:      action == QARCWait || qarcSpeechAllowed(observation),
			Reason:              reason,
		},
	}
}

func qarcActionInSet(action QARCAction, actions []QARCAction) bool {
	for _, candidate := range actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func qarcSpeechAllowed(observation QARCObservation) bool {
	return validQARCObservation(observation) &&
		observation.ScopeBound &&
		!observation.HesitationOnly &&
		(observation.EndpointCommitted || observation.CommitProtected) &&
		observation.UserOnsetProbability < 0.35 &&
		observation.IncidentalProbability <= 0.8 &&
		observation.AudioCancelable
}

func qarcOperatorSlot(operator QARCOperator) (QARCCueSlot, bool) {
	_, slot, ok := qarcOperatorTemplate(operator)
	return slot, ok
}

func qarcOperatorTemplate(
	operator QARCOperator,
) (QARCTemplateID, QARCCueSlot, bool) {
	switch operator {
	case QARCOperatorBoolean:
		return QARCTemplateBoolean, QARCSlotPolarity, true
	case QARCOperatorChoice:
		return QARCTemplateChoice, QARCSlotSelection, true
	case QARCOperatorQuantity:
		return QARCTemplateQuantity, QARCSlotQuantity, true
	case QARCOperatorState:
		return QARCTemplateState, QARCSlotState, true
	case QARCOperatorCause:
		return QARCTemplateCause, QARCSlotCause, true
	case QARCOperatorPurpose:
		return QARCTemplatePurpose, QARCSlotPurpose, true
	case QARCOperatorProcedure:
		return QARCTemplateProcedure, QARCSlotProcedure, true
	case QARCOperatorDefinition:
		return QARCTemplateDefinition, QARCSlotDefinition, true
	case QARCOperatorComparison:
		return QARCTemplateComparison, QARCSlotComparison, true
	case QARCOperatorEvidence:
		return QARCTemplateEvidence, QARCSlotEvidence, true
	case QARCOperatorOpen:
		return QARCTemplateOpen, QARCSlotPosition, true
	default:
		return QARCTemplateNone, QARCSlotNone, false
	}
}

func qarcTemplateIsCompiled(
	action QARCAction,
	templateID QARCTemplateID,
	slot QARCCueSlot,
) bool {
	switch action {
	case QARCWait:
		return templateID == QARCTemplateNone && slot == QARCSlotNone
	case QARCOperatorCue:
		expectedSlot, ok := qarcTemplateSlot(templateID)
		return ok && slot == expectedSlot
	case QARCNeutralCue:
		return templateID == QARCTemplateNeutral &&
			slot == QARCSlotNone
	case QARCRelease:
		return templateID == QARCTemplateRelease &&
			slot == QARCSlotNone
	default:
		return false
	}
}

func qarcTemplateSlot(templateID QARCTemplateID) (QARCCueSlot, bool) {
	switch templateID {
	case QARCTemplateBoolean:
		return QARCSlotPolarity, true
	case QARCTemplateChoice:
		return QARCSlotSelection, true
	case QARCTemplateQuantity:
		return QARCSlotQuantity, true
	case QARCTemplateState:
		return QARCSlotState, true
	case QARCTemplateCause:
		return QARCSlotCause, true
	case QARCTemplatePurpose:
		return QARCSlotPurpose, true
	case QARCTemplateProcedure:
		return QARCSlotProcedure, true
	case QARCTemplateDefinition:
		return QARCSlotDefinition, true
	case QARCTemplateComparison:
		return QARCSlotComparison, true
	case QARCTemplateEvidence:
		return QARCSlotEvidence, true
	case QARCTemplateOpen:
		return QARCSlotPosition, true
	default:
		return QARCSlotNone, false
	}
}

func qarcActionPriority(action QARCAction) int {
	switch action {
	case QARCWait:
		return 0
	case QARCNeutralCue:
		return 1
	case QARCOperatorCue:
		return 2
	case QARCRelease:
		return 3
	default:
		return 4
	}
}

func qarcRound(value float64) float64 {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return value
	}
	return math.Round(value*1_000_000) / 1_000_000
}
