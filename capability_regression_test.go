package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestZCodeUnknownCapabilitiesPreserved(t *testing.T) {
	c := map[string]any{"properties": map[string]any{"contextWindow": int64(123), "inputFormat": map[string]any{"supportsImage": true, "custom": true}}, "optionSpecs": map[string]any{"reasoningLevel": map[string]any{"values": []any{"max"}, "custom": true}, "maxOutputTokens": map[string]any{"max": int64(456), "custom": true}}}
	config := map[string]any{"modelConfigRules": map[string]any{"providerModelRules": []any{map[string]any{"providerId": providerID, "modelId": "x", "config": c}}}}
	apply := func(item catalogItem) {
		applyZCodeModelRules(config, Params{Model: "x", Models: []string{"x"}, Catalog: []catalogItem{item}})
	}
	apply(catalogItem{ID: "x"})
	props := c["properties"].(map[string]any)
	specs := c["optionSpecs"].(map[string]any)
	if props["contextWindow"] != int64(123) || props["inputFormat"].(map[string]any)["supportsImage"] != true || specs["maxOutputTokens"].(map[string]any)["max"] != int64(456) {
		t.Fatal("unknown erased prior values")
	}
	if !reflect.DeepEqual(specs["reasoningLevel"].(map[string]any)["values"], []any{"max"}) {
		t.Fatal("unknown reasoning erased")
	}
	no, yes := false, true
	apply(catalogItem{ID: "x", SupportsImages: &no, SupportsReasoning: &no})
	if props["inputFormat"].(map[string]any)["supportsImage"] != false {
		t.Fatal("explicit false ignored")
	}
	apply(catalogItem{ID: "x", OnlyReasoning: &yes, ReasoningEfforts: []string{"none", "disabled", "max"}})
	if !reflect.DeepEqual(specs["reasoningLevel"].(map[string]any)["values"], []any{"max"}) {
		t.Fatal("only reasoning allowed off")
	}
	if specs["reasoningLevel"].(map[string]any)["custom"] != true {
		t.Fatal("custom field erased")
	}
}

func TestApplyRejectsUnverifiedCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"real","supports_images":true}]}`))
	}))
	defer srv.Close()
	body := `{"base":"` + srv.URL + `","key":"test","models":["fake"],"catalog":[{"id":"fake","supports_images":false}],"items":[{"id":"zcode","model":"fake"}]}`
	w := httptest.NewRecorder()
	handleApply(w, httptest.NewRequest("POST", "/api/apply", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unverified model accepted: %d", w.Code)
	}
}
