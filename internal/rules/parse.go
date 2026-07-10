package rules

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ParseYAML deserializes and validates a rules document.
func ParseYAML(raw []byte) (*RuleSet, error) {
	var set RuleSet
	if err := yaml.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("rules: parse yaml: %w", err)
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return &set, nil
}

// MarshalYAML re-emits a canonical YAML form (used before persisting).
func MarshalYAML(set *RuleSet) ([]byte, error) {
	return yaml.Marshal(set)
}
