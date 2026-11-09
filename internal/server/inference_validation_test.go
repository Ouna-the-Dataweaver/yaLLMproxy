package server

import (
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/proxy"
)

func TestEmbeddingInputContract(t *testing.T) {
	for _, input := range []string{`"text"`, `["a","b"]`, `[0,123,456]`, `[[1,2],[3]]`} {
		t.Run(input, func(t *testing.T) {
			payload, err := proxy.DecodeJSONBody([]byte(`{"input":` + input + `,"dimensions":4,"encoding_format":"base64"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEmbeddings(payload); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(input, "[0") && estimateRequestTokens(payload) != 3 {
				t.Fatalf("token estimate = %d", estimateRequestTokens(payload))
			}
			if strings.HasPrefix(input, "[[") && estimateRequestTokens(payload) != 3 {
				t.Fatalf("batch token estimate = %d", estimateRequestTokens(payload))
			}
		})
	}
	for _, body := range []string{
		`{}`, `{"input":null}`, `{"input":""}`, `{"input":[]}`, `{"input":[[]]}`,
		`{"input":["a",[1]]}`, `{"input":[[1],"a"]}`, `{"input":[1,-2]}`, `{"input":[1,2.5]}`,
		`{"input":[[true]]}`, `{"input":"a","dimensions":0}`, `{"input":"a","dimensions":1.5}`,
		`{"input":"a","encoding_format":"bytes"}`, `{"input":"a","stream":true}`,
	} {
		payload, err := proxy.DecodeJSONBody([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateEmbeddings(payload); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestDecisionContractsAndReservation(t *testing.T) {
	for _, tc := range []struct {
		body     string
		validate validator
	}{
		{`{"state":{"ticket":"Refund please"},"questions":{"billing":{"type":"noul","instructions":"Billing?"}}}`, validateSystemOne},
		{`{"input":"Refund please","questions":[{"type":"predicate","instructions":"Billing?"}]}`, validateDecisions},
		{`{"input":{"role":"user","content":"Refund please"},"questions":[{"instructions":"Billing?"}]}`, validateDecisions},
	} {
		payload, err := proxy.DecodeJSONBody([]byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if err := tc.validate(payload); err != nil {
			t.Fatal(err)
		}
		if estimateRequestTokens(payload) < 5 {
			t.Fatalf("decision estimated too few tokens: %d", estimateRequestTokens(payload))
		}
		payload["stream"] = true
		if err := tc.validate(payload); err == nil {
			t.Fatal("accepted decision streaming")
		}
	}
	if err := validateSystemOne(map[string]any{"state": "a", "questions": []any{}}); err == nil {
		t.Fatal("accepted array instead of named questions")
	}
	if err := validateDecisions(map[string]any{"input": "a", "questions": map[string]any{}}); err == nil {
		t.Fatal("accepted object instead of question array")
	}
}
