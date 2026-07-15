package scoresemantics

import (
	"math"

	"github.com/pax-beehive/paxm/internal/config"
)

const cosineDistanceMaximum = 2

// Normalize converts a provider's configured score semantics into paxm's
// higher-is-better relevance range [0,1]. Mem0's distance mode is the
// pgvector cosine distance domain [0,2], so its equivalent similarity is
// 1-distance/2. Similarity mode retains the historical paxm normalization for
// values outside the documented [0,1] backend range.
func Normalize(raw float64, semantics config.ScoreSemantics) float64 {
	if semantics == config.ScoreSemanticsDistance {
		return normalizeCosineDistance(raw)
	}
	return normalizeSimilarity(raw)
}

func RawScoreKind(provider string, semantics config.ScoreSemantics) string {
	return provider + "_" + string(semantics)
}

func normalizeCosineDistance(raw float64) float64 {
	if math.IsNaN(raw) || raw >= cosineDistanceMaximum {
		return 0
	}
	if raw <= 0 {
		return 1
	}
	return 1 - raw/cosineDistanceMaximum
}

func normalizeSimilarity(raw float64) float64 {
	if raw < 0 {
		return 0
	}
	if raw <= 1 {
		return raw
	}
	return 1 / (1 + raw)
}
