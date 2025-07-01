package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

// Config represents the application's configuration
type Config struct {
	Port    int      `yaml:"port"`
	AuthDir string   `yaml:"auth_dir"`
	Debug   bool     `yaml:"debug"`
	ApiKeys []string `yaml:"api_keys"`
}

// / LoadConfig loads the configuration from the specified file
func LoadConfig(configFile string) (*Config, error) {
	// Read the configuration file
	data, err := os.ReadFile(configFile)
	// If reading the file fails
	if err != nil {
		// Return an error
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the YAML data
	var config Config
	// If parsing the YAML data fails
	if err = yaml.Unmarshal(data, &config); err != nil {
		// Return an error
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Return the configuration
	return &config, nil
}
