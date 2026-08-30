package semanticshadow

import (
	"strings"
	"testing"
)

func TestCompareRelationsEnumeratesEveryFinitePair(t *testing.T) {
	relations := []string{RelationDirect, RelationRestatement, RelationUnresolved, RelationConflict}
	seen := make(map[Comparison]bool)
	for _, current := range relations {
		for _, shadow := range relations {
			comparison, err := CompareRelations(current, shadow)
			if err != nil {
				t.Fatalf("%s -> %s: %v", current, shadow, err)
			}
			seen[comparison] = true
		}
	}
	if len(seen) != 13 {
		t.Fatalf("comparison enum count = %d, want 13", len(seen))
	}
	for _, unknown := range []string{"", "caption", "DIRECT", "direct:user-answer"} {
		if _, err := CompareRelations(unknown, RelationDirect); err == nil {
			t.Fatalf("unknown current relation accepted: %q", unknown)
		}
		if _, err := CompareRelations(RelationDirect, unknown); err == nil {
			t.Fatalf("unknown shadow relation accepted: %q", unknown)
		}
	}
}

func TestEvaluationSummaryOneHundredThousandTracesAreDeterministic(t *testing.T) {
	relations := []string{RelationDirect, RelationRestatement, RelationUnresolved, RelationConflict}
	build := func() EvaluationSummary {
		var summary EvaluationSummary
		for index := 0; index < 100_000; index++ {
			current := relations[index%len(relations)]
			shadow := relations[(index*7+3)%len(relations)]
			if err := summary.Add(current, shadow); err != nil {
				t.Fatal(err)
			}
		}
		return summary
	}
	first := build()
	second := build()
	a, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("aggregate is not deterministic:\n%s\n%s", a, b)
	}
	if first.Total != 100_000 {
		t.Fatalf("total = %d", first.Total)
	}
	for _, forbidden := range []string{"caption", "audio", "transcript", "answer", "uid", "token", "reasoning", "digest"} {
		if strings.Contains(strings.ToLower(string(a)), forbidden) {
			t.Fatalf("aggregate schema contains forbidden field %q: %s", forbidden, a)
		}
	}
}

func TestEvaluationSummaryRejectsUnknownWithoutMutation(t *testing.T) {
	var summary EvaluationSummary
	if err := summary.Add(RelationDirect, RelationDirect); err != nil {
		t.Fatal(err)
	}
	before, _ := summary.CanonicalJSON()
	if err := summary.Add(RelationDirect, "本人の回答本文"); err == nil {
		t.Fatal("free-form relation was accepted")
	}
	after, _ := summary.CanonicalJSON()
	if string(before) != string(after) {
		t.Fatalf("rejected observation mutated summary: %s -> %s", before, after)
	}
}
