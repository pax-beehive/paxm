package config

import (
	"errors"
	"strings"
)

type ScoreSemantics string

const (
	ScoreSemanticsSimilarity ScoreSemantics = "similarity"
	ScoreSemanticsDistance   ScoreSemantics = "distance"
)

func NormalizeScoreSemantics(value string) ScoreSemantics {
	if strings.TrimSpace(value) == "" {
		return ScoreSemanticsSimilarity
	}
	return ScoreSemantics(strings.ToLower(strings.TrimSpace(value)))
}

func ParseScoreSemantics(value string) (ScoreSemantics, error) {
	semantics := NormalizeScoreSemantics(value)
	switch semantics {
	case ScoreSemanticsSimilarity, ScoreSemanticsDistance:
		return semantics, nil
	default:
		return "", errors.New("score_semantics must be similarity or distance")
	}
}
