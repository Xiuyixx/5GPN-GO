package config

import (
	"fmt"
	"os"
	"regexp"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

var envRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// Load reads a YAML file, expands ${ENV_VAR} placeholders from the process
// environment, and returns a validated Config.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	expanded := envRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := string(m[2 : len(m)-1])
		return []byte(os.Getenv(name))
	})

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&cfg); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &cfg, nil
}
