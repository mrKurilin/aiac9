package main

import (
	"context"
	"testing"
)

func TestLLMMemoryExtractorParsesStructuredUpdate(t *testing.T) {
	client := &captureClient{reply: "```json\n{\"short\":{\"set\":{\"topic\":\"memory\"},\"delete\":[]},\"working\":{\"set\":{},\"delete\":[]},\"long\":{\"set\":{\"profile.language\":\"ru\"},\"delete\":[]}}\n```"}
	update, err := (LLMMemoryExtractor{Client: client}).Extract(context.Background(), MemoryLayers{}, nil, "prompt", "answer")
	if err != nil {
		t.Fatal(err)
	}
	if update.Short.Set["topic"] != "memory" || update.Long.Set["profile.language"] != "ru" {
		t.Fatalf("update=%+v", update)
	}
	if len(client.calls) != 1 || len(client.calls[0]) != 2 || client.calls[0][0].Role != "system" {
		t.Fatal("extractor boundary is wrong")
	}
}

func TestMemoryUpdateIsAtomicInMemoryOnInvalidFact(t *testing.T) {
	layers := MemoryLayers{ShortTerm: map[string]string{}, Working: map[string]string{}, LongTerm: map[string]string{}}
	next := cloneLayers(layers)
	err := applyMemoryUpdate(&next, MemoryUpdate{Working: LayerUpdate{Set: map[string]string{"password": "should-not-save"}}})
	if err == nil {
		t.Fatal("invalid fact accepted")
	}
	if len(layers.Working) != 0 {
		t.Fatal("source layers changed")
	}
}
