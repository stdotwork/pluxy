package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/cmd"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"os"
	"path"
	"strings"
)

// LogFormatter defines a custom log format for logrus.
type LogFormatter struct {
}

// Format renders a single log entry.
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	var newLog string
	// Customize the log format to include timestamp, level, caller file/line, and message.
	newLog = fmt.Sprintf("[%s] [%s] [%s:%d] %s\n", timestamp, entry.Level, path.Base(entry.Caller.File), entry.Caller.Line, entry.Message)

	b.WriteString(newLog)
	return b.Bytes(), nil
}

// init initializes the logger configuration.
func init() {
	// Set logger output to standard output.
	log.SetOutput(os.Stdout)
	// Enable reporting the caller function's file and line number.
	log.SetReportCaller(true)
	// Set the custom log formatter.
	log.SetFormatter(&LogFormatter{})
}

// main is the entry point of the application.
func main() {
	var login bool
	var projectID string
	var configPath string

	// Define command-line flags.
	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.StringVar(&projectID, "project_id", "", "Project ID")
	flag.StringVar(&configPath, "config", "", "Configure File Path")

	// Parse the command-line flags.
	flag.Parse()

	var err error
	var cfg *config.Config
	var wd string

	// Load configuration from the specified path or the default path.
	var configFilePath string
	if configPath != "" {
		configFilePath = configPath
		cfg, err = config.LoadConfig(configPath)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		configFilePath = path.Join(wd, "config.yaml")
		cfg, err = config.LoadConfig(configFilePath)
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Set the log level based on the configuration.
	if cfg.Debug {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	// Expand the tilde (~) in the auth directory path to the user's home directory.
	if strings.HasPrefix(cfg.AuthDir, "~") {
		home, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			log.Fatalf("failed to get home directory: %v", errUserHomeDir)
		}
		parts := strings.Split(cfg.AuthDir, string(os.PathSeparator))
		if len(parts) > 1 {
			parts[0] = home
			cfg.AuthDir = path.Join(parts...)
		} else {
			cfg.AuthDir = home
		}
	}

	// Either perform login or start the service based on the 'login' flag.
	if login {
		cmd.DoLogin(cfg, projectID)
	} else {
		cmd.StartService(cfg, configFilePath)
	}
}
