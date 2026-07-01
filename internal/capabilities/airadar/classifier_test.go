package airadar

import (
	"log"
	"math"
	"testing"
)

const fixtureModel = `{
  "analyzer": "char",
  "ngram_range": [1, 3],
  "lowercase": true,
  "norm": "l2",
  "intercept": -1,
  "threshold": 0.6,
  "ngrams": {
    "a": [2, 1],
    "你": [3, -1],
    "好": [4, -2],
    "你好": [5, -3],
    "😀": [7, 2],
    "a你": [11, 4],
    "a你好": [13, 5]
  }
}`

func TestClassifierPredictMatchesTfidfLogisticFormula(t *testing.T) {
	classifier, err := NewClassifierFromJSON([]byte(fixtureModel))
	if err != nil {
		t.Fatal(err)
	}
	got := classifier.Proba("A你好")
	norm := math.Sqrt(4 + 9 + 16 + 25 + 121 + 169)
	score := -1.0 + 2/norm*1 + 3/norm*-1 + 4/norm*-2 + 5/norm*-3 + 11/norm*4 + 13/norm*5
	want := 1 / (1 + math.Exp(-score))
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("prob mismatch: got %.15f want %.15f", got, want)
	}
	result := classifier.Predict("A你好")
	if !result.IsAI || result.Label != "AI味" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClassifierPreprocessLowercaseWhitespaceAndCodePoints(t *testing.T) {
	classifier, err := NewClassifierFromJSON([]byte(fixtureModel))
	if err != nil {
		t.Fatal(err)
	}
	chars := classifier.preprocess("A \t\n😀")
	want := []string{"a", " ", "😀"}
	if len(chars) != len(want) {
		t.Fatalf("chars length = %d, want %d: %#v", len(chars), len(want), chars)
	}
	for i := range want {
		if chars[i] != want[i] {
			t.Fatalf("chars[%d] = %q, want %q", i, chars[i], want[i])
		}
	}
}

func TestDefaultClassifierLoadsEmbeddedModel(t *testing.T) {
	classifier, err := NewDefaultClassifier()
	if err != nil {
		t.Fatal(err)
	}
	result := classifier.Predict("楠楠说别不寄 三个字 比我回她的五句话加起来都重")
	log.Println(result)
	if result.Prob < 0 || result.Prob > 1 {
		t.Fatalf("prob out of range: %+v", result)
	}
	if result.Threshold != 0.6 {
		t.Fatalf("threshold = %v, want 0.6", result.Threshold)
	}
}
