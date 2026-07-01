package airadar

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode"
)

//go:embed model.json
var defaultModelData []byte

type Model struct {
	Analyzer   string                `json:"analyzer"`
	NgramRange [2]int                `json:"ngram_range"`
	Lowercase  bool                  `json:"lowercase"`
	Norm       string                `json:"norm"`
	Intercept  float64               `json:"intercept"`
	Threshold  float64               `json:"threshold"`
	Ngrams     map[string]NgramEntry `json:"ngrams"`
}

type NgramEntry struct {
	IDF  float64
	Coef float64
}

func (e *NgramEntry) UnmarshalJSON(data []byte) error {
	var values []float64
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if len(values) != 2 {
		return errors.New("ngram entry must contain [idf, coef]")
	}
	e.IDF = values[0]
	e.Coef = values[1]
	return nil
}

type Classifier struct {
	model Model
	nMin  int
	nMax  int
}

type Result struct {
	Prob      float64 `json:"prob"`
	IsAI      bool    `json:"isAI"`
	Label     string  `json:"label"`
	Threshold float64 `json:"threshold"`
}

func NewDefaultClassifier() (*Classifier, error) {
	return NewClassifierFromJSON(defaultModelData)
}

func NewClassifierFromJSON(data []byte) (*Classifier, error) {
	var model Model
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, err
	}
	if len(model.Ngrams) == 0 {
		return nil, errors.New("airadar model has empty ngram vocabulary")
	}
	nMin, nMax := model.NgramRange[0], model.NgramRange[1]
	if nMin <= 0 || nMax < nMin {
		return nil, errors.New("airadar model has invalid ngram_range")
	}
	if model.Threshold == 0 {
		model.Threshold = 0.6
	}
	return &Classifier{model: model, nMin: nMin, nMax: nMax}, nil
}

func (c *Classifier) Proba(text string) float64 {
	if c == nil {
		return 0
	}
	chars := c.preprocess(text)
	counts := map[string]int{}
	order := []string{}
	for n := c.nMin; n <= c.nMax; n++ {
		for i := 0; i+n <= len(chars); i++ {
			gram := strings.Join(chars[i:i+n], "")
			if _, ok := c.model.Ngrams[gram]; ok {
				if counts[gram] == 0 {
					order = append(order, gram)
				}
				counts[gram]++
			}
		}
	}

	type weightedTerm struct {
		value float64
		coef  float64
	}
	var norm float64
	terms := make([]weightedTerm, 0, len(counts))
	for _, gram := range order {
		count := counts[gram]
		entry := c.model.Ngrams[gram]
		value := float64(count) * entry.IDF
		norm += value * value
		terms = append(terms, weightedTerm{value: value, coef: entry.Coef})
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}

	score := c.model.Intercept
	for _, term := range terms {
		score += (term.value / norm) * term.coef
	}
	return 1 / (1 + math.Exp(-score))
}

func (c *Classifier) Predict(text string, threshold ...float64) Result {
	activeThreshold := c.model.Threshold
	if len(threshold) > 0 && threshold[0] > 0 {
		activeThreshold = threshold[0]
	}
	prob := c.Proba(text)
	label := "人味"
	isAI := prob >= activeThreshold
	if isAI {
		label = "AI味"
	}
	return Result{Prob: prob, IsAI: isAI, Label: label, Threshold: activeThreshold}
}

func (c *Classifier) preprocess(text string) []string {
	if c.model.Lowercase {
		text = strings.ToLower(text)
	}
	text = collapseRepeatedWhitespace(text)
	chars := make([]string, 0, len(text))
	for _, r := range text {
		chars = append(chars, string(r))
	}
	return chars
}

func collapseRepeatedWhitespace(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	var whitespaceRun []rune
	flushWhitespace := func() {
		switch len(whitespaceRun) {
		case 0:
		case 1:
			builder.WriteRune(whitespaceRun[0])
		default:
			builder.WriteByte(' ')
		}
		whitespaceRun = whitespaceRun[:0]
	}
	for _, r := range text {
		if unicode.IsSpace(r) {
			whitespaceRun = append(whitespaceRun, r)
			continue
		}
		flushWhitespace()
		builder.WriteRune(r)
	}
	flushWhitespace()
	return builder.String()
}
