package scoresemantics

import (
	"math"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
)

func TestNormalizeScoreSemanticsTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		semantics config.ScoreSemantics
		raw       float64
		want      float64
	}{
		{name: "similarity", semantics: config.ScoreSemanticsSimilarity, raw: 0.82, want: 0.82},
		{name: "similarity negative", semantics: config.ScoreSemanticsSimilarity, raw: -1, want: 0},
		{name: "similarity legacy large", semantics: config.ScoreSemanticsSimilarity, raw: 2, want: 1.0 / 3.0},
		{name: "distance exact match", semantics: config.ScoreSemanticsDistance, raw: 0, want: 1},
		{name: "distance low", semantics: config.ScoreSemanticsDistance, raw: 0.479, want: 0.7605},
		{name: "distance high", semantics: config.ScoreSemanticsDistance, raw: 0.840, want: 0.58},
		{name: "distance maximum", semantics: config.ScoreSemanticsDistance, raw: 2, want: 0},
		{name: "distance beyond maximum", semantics: config.ScoreSemanticsDistance, raw: 3, want: 0},
		{name: "distance negative", semantics: config.ScoreSemanticsDistance, raw: -1, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.raw, tt.semantics); math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("Normalize(%v, %q) = %v, want %v", tt.raw, tt.semantics, got, tt.want)
			}
		})
	}
}

func TestRawScoreKindUsesConfiguredSemantics(t *testing.T) {
	t.Parallel()
	for _, semantics := range []config.ScoreSemantics{config.ScoreSemanticsSimilarity, config.ScoreSemanticsDistance} {
		if got := RawScoreKind("mem0", semantics); got != "mem0_"+string(semantics) {
			t.Fatalf("RawScoreKind() = %q, want %q", got, "mem0_"+string(semantics))
		}
	}
}
