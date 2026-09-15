// Package yamlutil provides small helpers for navigating and mutating yaml.Node trees.
package yamlutil

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// DocumentMapping returns the root mapping node of a YAML document.
func DocumentMapping(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		if root.Content[0].Kind == yaml.MappingNode {
			return root.Content[0]
		}
		return nil
	}
	if root.Kind == yaml.MappingNode {
		return root
	}
	return nil
}

// MappingEntry returns the value node for key in a mapping node.
func MappingEntry(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

// MappingValue is an alias of MappingEntry.
func MappingValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	return MappingEntry(node, key)
}

// ScalarValue decodes the scalar string value for key in a mapping node.
func ScalarValue(node *yaml.Node, key string) (string, bool) {
	child, ok := MappingEntry(node, key)
	if !ok {
		return "", false
	}
	value, err := DecodeScalar(child)
	if err != nil {
		return "", false
	}
	return value, true
}

// RemoveMappingKey deletes key from a mapping node if present.
func RemoveMappingKey(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// DecodeScalar decodes a YAML node as a string.
func DecodeScalar(node *yaml.Node) (string, error) {
	var value string
	if err := node.Decode(&value); err != nil {
		return "", err
	}
	return value, nil
}

// DecodeStringSlice decodes a YAML node as a string or list of strings.
func DecodeStringSlice(node *yaml.Node) ([]string, error) {
	if node == nil {
		return nil, nil
	}
	var single string
	if err := node.Decode(&single); err == nil {
		return []string{single}, nil
	}
	var multi []string
	if err := node.Decode(&multi); err != nil {
		return nil, fmt.Errorf("expected string or string list")
	}
	return multi, nil
}
