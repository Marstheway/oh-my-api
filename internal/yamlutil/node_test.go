package yamlutil

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMappingHelpers(t *testing.T) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("a: one\nb:\n  - x\n  - y\n"), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	doc := DocumentMapping(&root)
	if doc == nil {
		t.Fatal("expected document mapping")
	}

	val, ok := MappingValue(doc, "a")
	if !ok {
		t.Fatal("expected key a")
	}
	got, err := DecodeScalar(val)
	if err != nil || got != "one" {
		t.Fatalf("DecodeScalar = %q, %v; want one", got, err)
	}

	seq, ok := MappingEntry(doc, "b")
	if !ok || seq.Kind != yaml.SequenceNode {
		t.Fatal("expected sequence b")
	}
	items, err := DecodeStringSlice(seq)
	if err != nil {
		t.Fatalf("DecodeStringSlice: %v", err)
	}
	if len(items) != 2 || items[0] != "x" || items[1] != "y" {
		t.Fatalf("items = %v, want [x y]", items)
	}

	RemoveMappingKey(doc, "a")
	if _, ok := MappingEntry(doc, "a"); ok {
		t.Fatal("expected key a removed")
	}
}
