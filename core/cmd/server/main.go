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

type LogFormatter struct {
}

func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	var newLog string
	newLog = fmt.Sprintf("[%s] [%s] [%s:%d] %s\n", timestamp, entry.Level, path.Base(entry.Caller.File), entry.Caller.Line, entry.Message)

	b.WriteString(newLog)
	return b.Bytes(), nil
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetReportCaller(true)
	log.SetFormatter(&LogFormatter{})
}

func main() {
	var login bool
	var projectID string
	var configPath string

	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.StringVar(&projectID, "project_id", "", "Project ID")
	flag.StringVar(&configPath, "config", "", "Configure File Path")

	flag.Parse()

	var err error
	var cfg *config.Config
	var wd string

	if configPath != "" {
		cfg, err = config.LoadConfig(configPath)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		cfg, err = config.LoadConfig(path.Join(wd, "config.yaml"))
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if cfg.Debug {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

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

	if login {
		cmd.DoLogin(cfg, projectID)
	} else {
		cmd.StartService(cfg)
	}
}
