package retrieval

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	minExcerptBytes        = 8192
	maxExcerptBytes        = 8000
	maxExcerptBytesPerHit  = 2400
	maxExcerptSegmentBytes = 800
	maxExcerptSegments     = 3
	excerptNeighbors       = 1
)

type scoredSegment struct {
	index    int
	matched  int
	phrase   bool
	temporal bool
	density  float64
}

func excerptHits(query string, hits []Hit) []Hit {
	if len(hits) == 0 {
		return hits
	}
	queryTerms := excerptQueryTerms(query)
	if len(queryTerms) == 0 {
		return hits
	}
	budget := min(maxExcerptBytesPerHit, maxExcerptBytes/len(hits))
	for i := range hits {
		if len(hits[i].Text) <= minExcerptBytes {
			continue
		}
		if excerptMatchedTermCount(queryTerms, hits[i].Text) < len(queryTerms) {
			continue
		}
		excerpt := excerptText(query, queryTerms, hits[i].Text, budget)
		if excerpt == "" || excerpt == hits[i].Text {
			continue
		}
		if excerptMatchedTermCount(queryTerms, excerpt) < len(queryTerms) {
			continue
		}
		originalBytes := len(hits[i].Text)
		hits[i].Text = excerpt
		hits[i].Metadata = copyHitMetadata(hits[i].Metadata)
		hits[i].Metadata["sqlite_excerpted"] = "true"
		hits[i].Metadata["sqlite_original_bytes"] = strconv.Itoa(originalBytes)
	}
	return hits
}

func excerptQueryTerms(query string) []string {
	terms := uniqueStrings(normalizeTerms(query))
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		if excerptStopWord(term) || shortASCIIExcerptTerm(term) {
			continue
		}
		result = append(result, term)
	}
	return uniqueStrings(result)
}

func excerptStopWord(term string) bool {
	switch term {
	case "a", "an", "and", "are", "as", "at", "be", "been", "did", "do", "does", "for", "from", "had", "has", "have", "her", "his", "how", "in", "is", "it", "its", "long", "many", "my", "of", "on", "or", "our", "the", "their", "to", "was", "were", "what", "when", "where", "which", "who", "why", "will", "with", "would", "your":
		return true
	default:
		return false
	}
}

func shortASCIIExcerptTerm(term string) bool {
	if len(term) >= 3 {
		return false
	}
	for _, value := range term {
		if value > 127 {
			return false
		}
	}
	return true
}

func excerptText(query string, queryTerms []string, text string, budget int) string {
	segments := excerptSegments(text)
	if len(segments) < 2 {
		return text
	}
	headerIndex := excerptSessionHeaderIndex(segments)
	wantsTemporalEvidence := excerptTemporalQuery(query)
	scored := scoreExcerptSegments(query, queryTerms, segments, headerIndex, wantsTemporalEvidence)
	if len(scored) == 0 {
		return text
	}
	sortExcerptSegments(scored, wantsTemporalEvidence)
	selected := selectExcerptSegments(segments, scored, headerIndex, budget)
	return renderExcerptSegments(segments, selected)
}

func excerptSessionHeaderIndex(segments []string) int {
	if len(segments) > 0 && isExcerptSessionHeader(segments[0]) {
		return 0
	}
	return -1
}

func scoreExcerptSegments(query string, queryTerms, segments []string, headerIndex int, wantsTemporalEvidence bool) []scoredSegment {
	scored := make([]scoredSegment, 0, len(segments))
	for i, segment := range segments {
		if i == headerIndex {
			continue
		}
		matched := excerptMatchedTermCount(queryTerms, segment)
		if matched == 0 {
			continue
		}
		temporal := wantsTemporalEvidence && excerptHasTemporalEvidence(segment)
		scored = append(scored, scoredSegment{
			index: i, matched: matched, phrase: excerptContainsPhrase(query, queryTerms, segment),
			temporal: temporal, density: excerptEvidenceDensity(matched, segment),
		})
	}
	return scored
}

func sortExcerptSegments(scored []scoredSegment, wantsTemporalEvidence bool) {
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].phrase != scored[j].phrase {
			return scored[i].phrase
		}
		if wantsTemporalEvidence && scored[i].temporal != scored[j].temporal {
			return scored[i].temporal
		}
		if scored[i].matched != scored[j].matched {
			return scored[i].matched > scored[j].matched
		}
		if scored[i].density != scored[j].density {
			return scored[i].density > scored[j].density
		}
		return scored[i].index < scored[j].index
	})
}

func selectExcerptSegments(segments []string, scored []scoredSegment, headerIndex, budget int) []int {
	selected := make(map[int]struct{}, maxExcerptSegments*(excerptNeighbors*2+1))
	selectedBytes := 0
	add := func(index int) bool {
		if _, ok := selected[index]; ok {
			return true
		}
		const separatorAllowance = len("\n[...]\n")
		if selectedBytes+len(segments[index])+separatorAllowance > budget {
			return false
		}
		selected[index] = struct{}{}
		selectedBytes += len(segments[index]) + separatorAllowance
		return true
	}
	if headerIndex >= 0 {
		add(headerIndex)
	}
	for _, candidate := range scored[:min(len(scored), maxExcerptSegments)] {
		if !add(candidate.index) {
			continue
		}
		for distance := 1; distance <= excerptNeighbors; distance++ {
			if before := candidate.index - distance; before >= 0 {
				add(before)
			}
			if after := candidate.index + distance; after < len(segments) {
				add(after)
			}
		}
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func renderExcerptSegments(segments []string, indices []int) string {
	var result strings.Builder
	previous := -2
	for _, index := range indices {
		separator := "\n"
		if result.Len() == 0 {
			separator = ""
		} else if index != previous+1 {
			separator = "\n[...]\n"
		}
		addition := separator + segments[index]
		result.WriteString(addition)
		previous = index
	}
	return strings.TrimSpace(result.String())
}

func isExcerptSessionHeader(segment string) bool {
	segment = strings.TrimSpace(segment)
	return strings.HasPrefix(segment, "[") && strings.HasSuffix(segment, "]")
}

func excerptTemporalQuery(query string) bool {
	query = strings.ToLower(query)
	for _, marker := range []string{"when", "how long", "how many", "what date", "what day", "what year"} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func excerptHasTemporalEvidence(text string) bool {
	for _, value := range text {
		if value >= '0' && value <= '9' {
			return true
		}
	}
	text = strings.ToLower(text)
	for _, marker := range []string{
		"yesterday", "last week", "last month", "last year", "years ago", "months ago", "weeks ago", "days ago",
		"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func excerptMatchedTermCount(queryTerms []string, text string) int {
	textSet := make(map[string]struct{})
	for _, term := range normalizeTerms(text) {
		textSet[term] = struct{}{}
	}
	lowerText := strings.ToLower(text)
	matched := 0
	for _, term := range queryTerms {
		if _, ok := textSet[term]; ok || strings.Contains(lowerText, term) {
			matched++
		}
	}
	return matched
}

func excerptContainsPhrase(query string, queryTerms []string, text string) bool {
	if strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query))) {
		return true
	}
	textTerms := make([]string, 0)
	for _, term := range normalizeTerms(text) {
		textTerms = append(textTerms, term)
	}
	return strings.Contains(" "+strings.Join(textTerms, " ")+" ", " "+strings.Join(queryTerms, " ")+" ")
}

func excerptEvidenceDensity(matched int, text string) float64 {
	terms := make([]string, 0)
	for _, term := range normalizeTerms(text) {
		terms = append(terms, term)
	}
	unique := uniqueStrings(terms)
	if len(unique) == 0 {
		return 0
	}
	return float64(matched) / float64(len(unique))
}

func excerptSegments(text string) []string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) <= maxExcerptSegmentBytes {
			result = append(result, line)
			continue
		}
		result = append(result, splitLongExcerptLine(line)...)
	}
	return result
}

func splitLongExcerptLine(line string) []string {
	prefix, content := excerptLinePrefix(line)
	runes := []rune(content)
	result := make([]string, 0, len(runes)/80+1)
	start := 0
	for i, current := range runes {
		if !excerptSentenceEnd(current) {
			continue
		}
		if (current == '.' || current == ';') && i+1 < len(runes) && !runeIsSpace(runes[i+1]) {
			continue
		}
		if segment := strings.TrimSpace(string(runes[start : i+1])); segment != "" {
			result = append(result, chunkExcerptSegment(prefix, segment)...)
		}
		start = i + 1
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		result = append(result, chunkExcerptSegment(prefix, tail)...)
	}
	if len(result) == 0 {
		return []string{line}
	}
	return result
}

func chunkExcerptSegment(prefix, segment string) []string {
	if len(prefix)+len(segment) <= maxExcerptSegmentBytes {
		return []string{prefix + segment}
	}
	words := strings.Fields(segment)
	if len(words) < 2 {
		return chunkUnspacedExcerptSegment(prefix, segment)
	}
	result := make([]string, 0, len(segment)/maxExcerptSegmentBytes+1)
	var chunk strings.Builder
	flush := func() {
		if chunk.Len() == 0 {
			return
		}
		result = append(result, prefix+chunk.String())
		chunk.Reset()
	}
	contentBudget := max(1, maxExcerptSegmentBytes-len(prefix))
	for _, word := range words {
		addition := len(word)
		if chunk.Len() > 0 {
			addition++
		}
		if chunk.Len() > 0 && chunk.Len()+addition > contentBudget {
			flush()
		}
		if chunk.Len() > 0 {
			chunk.WriteByte(' ')
		}
		chunk.WriteString(word)
	}
	flush()
	return result
}

func chunkUnspacedExcerptSegment(prefix, segment string) []string {
	contentBudget := max(1, maxExcerptSegmentBytes-len(prefix))
	runes := []rune(segment)
	result := make([]string, 0, len(segment)/maxExcerptSegmentBytes+1)
	for start := 0; start < len(runes); {
		end := start
		bytes := 0
		for end < len(runes) {
			next := utf8.RuneLen(runes[end])
			if end > start && bytes+next > contentBudget {
				break
			}
			bytes += next
			end++
		}
		result = append(result, prefix+string(runes[start:end]))
		if end == len(runes) {
			break
		}
		const overlapRunes = 32
		start = max(start+1, end-overlapRunes)
	}
	return result
}

func excerptLinePrefix(line string) (string, string) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 || colon > 64 {
		return "", line
	}
	prefix := strings.TrimSpace(line[:colon])
	if strings.ContainsAny(prefix, ".!?;。！？；") {
		return "", line
	}
	return prefix + ": ", strings.TrimSpace(line[colon+1:])
}

func excerptSentenceEnd(value rune) bool {
	return strings.ContainsRune(".!?;。！？；", value)
}

func runeIsSpace(value rune) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func copyHitMetadata(metadata map[string]string) map[string]string {
	result := make(map[string]string, len(metadata)+2)
	for key, value := range metadata {
		result[key] = value
	}
	return result
}
