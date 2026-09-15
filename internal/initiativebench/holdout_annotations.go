package initiativebench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
)

const (
	HoldoutAnnotationSchemaVersion = "kotae.mixed-initiative-holdout-annotations.v1"
	minimumConsensusBPS            = 6_667
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type AnnotationChoice string

const ChoiceAmbiguous AnnotationChoice = "ambiguous"

type RaterAnnotation struct {
	RaterSlot uint8            `json:"rater_slot"`
	Choice    AnnotationChoice `json:"choice"`
}

type AnnotatedHoldoutItem struct {
	FixtureID   string            `json:"fixture_id"`
	Situation   Situation         `json:"situation"`
	Annotations []RaterAnnotation `json:"annotations"`
}

type HoldoutAnnotationSet struct {
	SchemaVersion string                 `json:"schema_version"`
	SourceSHA256  string                 `json:"source_sha256"`
	BlindingSeed  uint64                 `json:"blinding_seed"`
	Items         []AnnotatedHoldoutItem `json:"items"`
}

type HoldoutConsensus struct {
	FixtureID      string  `json:"fixture_id"`
	ExpectedAction *Action `json:"expected_action"`
	RaterCount     int     `json:"rater_count"`
	DecisiveRaters int     `json:"decisive_raters"`
	AgreementBPS   *int    `json:"agreement_basis_points"`
	Ambiguous      bool    `json:"ambiguous"`
}

type HoldoutAgreementReport struct {
	SchemaVersion      string             `json:"schema_version"`
	SourceSHA256       string             `json:"source_sha256"`
	BlindingSeed       uint64             `json:"blinding_seed"`
	ItemCount          int                `json:"item_count"`
	AnnotationCount    int                `json:"annotation_count"`
	AmbiguousItemCount int                `json:"ambiguous_item_count"`
	NominalAlphaBPS    *int               `json:"nominal_alpha_basis_points"`
	Consensus          []HoldoutConsensus `json:"consensus"`
}

func DecodeHoldoutAnnotations(reader io.Reader) (HoldoutAnnotationSet, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 16<<20))
	decoder.DisallowUnknownFields()
	var set HoldoutAnnotationSet
	if err := decoder.Decode(&set); err != nil {
		return HoldoutAnnotationSet{}, fmt.Errorf("initiative holdout decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HoldoutAnnotationSet{}, errors.New("initiative holdout trailing data")
	}
	if err := set.Validate(); err != nil {
		return HoldoutAnnotationSet{}, err
	}
	return set, nil
}

func (set HoldoutAnnotationSet) Validate() error {
	if set.SchemaVersion != HoldoutAnnotationSchemaVersion || !sha256Pattern.MatchString(set.SourceSHA256) ||
		set.BlindingSeed == 0 || len(set.Items) == 0 || len(set.Items) > 10_000 {
		return errors.New("initiative holdout annotation set invalid")
	}
	fixtureIDs := make(map[string]struct{}, len(set.Items))
	for _, item := range set.Items {
		if !validID(item.FixtureID) || !validSituation(item.Situation) ||
			len(item.Annotations) < 2 || len(item.Annotations) > 20 {
			return errors.New("initiative holdout item invalid")
		}
		if _, exists := fixtureIDs[item.FixtureID]; exists {
			return errors.New("initiative holdout fixture duplicated")
		}
		fixtureIDs[item.FixtureID] = struct{}{}
		slots := make(map[uint8]struct{}, len(item.Annotations))
		for _, annotation := range item.Annotations {
			if annotation.RaterSlot < 1 || annotation.RaterSlot > 20 || !validAnnotationChoice(annotation.Choice) {
				return errors.New("initiative holdout rater annotation invalid")
			}
			if _, exists := slots[annotation.RaterSlot]; exists {
				return errors.New("initiative holdout rater slot duplicated")
			}
			slots[annotation.RaterSlot] = struct{}{}
		}
	}
	return nil
}

func EvaluateHoldoutAgreement(set HoldoutAnnotationSet) (HoldoutAgreementReport, error) {
	if err := set.Validate(); err != nil {
		return HoldoutAgreementReport{}, err
	}
	report := HoldoutAgreementReport{
		SchemaVersion: HoldoutAnnotationSchemaVersion,
		SourceSHA256:  set.SourceSHA256, BlindingSeed: set.BlindingSeed,
		ItemCount: len(set.Items), Consensus: make([]HoldoutConsensus, 0, len(set.Items)),
	}
	globalCounts := make(map[Action]int64)
	var observedDisagreements, observedPairs, totalDecisive int64
	for _, item := range set.Items {
		counts := make(map[Action]int)
		explicitAmbiguous := false
		for _, annotation := range item.Annotations {
			report.AnnotationCount++
			if annotation.Choice == ChoiceAmbiguous {
				explicitAmbiguous = true
				continue
			}
			action := Action(annotation.Choice)
			counts[action]++
			globalCounts[action]++
			totalDecisive++
		}
		decisive := 0
		top := 0
		topActions := 0
		for _, action := range []Action{ActionWait, ActionAskOne, ActionReflectUserGoal, ActionOfferChoice, ActionStartPractice, ActionRelease, ActionStop} {
			count := counts[action]
			decisive += count
			if count > top {
				top, topActions = count, 1
			} else if count > 0 && count == top {
				topActions++
			}
		}
		consensus := HoldoutConsensus{FixtureID: item.FixtureID, RaterCount: len(item.Annotations), DecisiveRaters: decisive}
		if decisive > 0 {
			agreement := (top*10_000 + decisive/2) / decisive
			consensus.AgreementBPS = &agreement
			ambiguous := explicitAmbiguous || decisive < 2 || topActions != 1 || agreement < minimumConsensusBPS
			consensus.Ambiguous = ambiguous
			if !ambiguous {
				for action, count := range counts {
					if count == top {
						selected := action
						consensus.ExpectedAction = &selected
						break
					}
				}
			}
		} else {
			consensus.Ambiguous = true
		}
		if consensus.Ambiguous {
			report.AmbiguousItemCount++
		}
		report.Consensus = append(report.Consensus, consensus)
		if decisive >= 2 {
			var samePairs int64
			for _, count := range counts {
				samePairs += int64(count * (count - 1))
			}
			pairs := int64(decisive * (decisive - 1))
			observedPairs += pairs
			observedDisagreements += pairs - samePairs
		}
	}
	if observedPairs > 0 && totalDecisive > 1 {
		var squaredCounts int64
		for _, count := range globalCounts {
			squaredCounts += count * count
		}
		expectedDisagreements := totalDecisive*totalDecisive - squaredCounts
		if expectedDisagreements > 0 {
			expectedPairs := totalDecisive * (totalDecisive - 1)
			numerator := observedPairs*expectedDisagreements - observedDisagreements*expectedPairs
			denominator := observedPairs * expectedDisagreements
			alpha := roundedBasisPoints(numerator, denominator)
			report.NominalAlphaBPS = &alpha
		}
	}
	return report, nil
}

func validAnnotationChoice(choice AnnotationChoice) bool {
	return choice == ChoiceAmbiguous || validAction(Action(choice))
}

func roundedBasisPoints(numerator, denominator int64) int {
	scaled := new(big.Int).Mul(big.NewInt(numerator), big.NewInt(10_000))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled, big.NewInt(denominator), remainder)
	absRemainder := new(big.Int).Abs(remainder)
	if absRemainder.Mul(absRemainder, big.NewInt(2)).Cmp(big.NewInt(denominator)) >= 0 {
		if scaled.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	return int(quotient.Int64())
}
