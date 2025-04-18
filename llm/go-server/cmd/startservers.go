package main

import (
	"fmt"
	"github.com/dubbogo/gost/log/logger"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	runningProcesses []*os.Process
	shutdownChan     = make(chan os.Signal, 1)
)

type GlobalConfig struct {
	GatewayServiceName string `yaml:"gateway_service_name"`
	ModelServiceGroup  string `yaml:"model_service_group"`
	Port               int    `yaml:"port"`
}

// Config represents the root configuration structure
type Config struct {
	Global       GlobalConfig  `yaml:"global"`
	NacosConfig  NacosConfig   `yaml:"nacos_config"`
	ServerGroups []ServerGroup `yaml:"server_groups"`
	HealthCheck  HealthCheck   `yaml:"health_check"`
	RoutingRules []RoutingRule `yaml:"routing_rules"`
	AutoScaling  AutoScaling   `yaml:"autoscaling"`
}

type NacosConfig struct {
	ServerAddr string `yaml:"server_addr"`
}

type ServerGroup struct {
	ModelName     string         `yaml:"model_name"`
	Provider      string         `yaml:"provider"`
	BaseURL       string         `yaml:"base_url"`
	InitInstances int            `yaml:"init_instances"`
	Port          int            `yaml:"port"`
	Metadata      map[string]int `yaml:"metadata,omitempty"`
}

type HealthCheck struct {
	Interval      string `yaml:"interval"`
	Timeout       string `yaml:"timeout"`
	MaxRetries    int    `yaml:"max_retries"`
	GracePeriod   string `yaml:"grace_period"`
	FailureAction string `yaml:"failure_action"`
}

type RoutingRule struct {
	ModelName  string `yaml:"model_name"`
	Strategy   string `yaml:"strategy"`
	FallbackTo string `yaml:"fallback_to"`
	Timeout    string `yaml:"timeout"`
}

type AutoScaling struct {
	Metrics      []Metric `yaml:"metrics"`
	Cooldown     string   `yaml:"cooldown"`
	MinInstances int      `yaml:"min_instances"`
	MaxInstances int      `yaml:"max_instances"`
}

type Metric struct {
	Type         string `yaml:"type"`
	Threshold    int    `yaml:"threshold"`
	ScaleOutStep int    `yaml:"scale_out_step"`
	ScaleInStep  int    `yaml:"scale_in_step"`
}

// LoadConfig loads configuration from the specified YAML file
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

func startServer(group ServerGroup, instanceID int) error {
	actualPort := group.Port + instanceID
	logger.Infof("Starting server instance %d for model %s on port %d", instanceID, group.ModelName, actualPort)

	// Set environment variables based on config
	env := []string{
		fmt.Sprintf("OLLAMA_MODEL=%s", group.ModelName),
		fmt.Sprintf("OLLAMA_URL=%s", group.BaseURL),
		fmt.Sprintf("PROVIDER=%s", group.Provider),
		fmt.Sprintf("SERVICE_PORT=%d", actualPort),
		fmt.Sprintf("WEIGHT=%d", group.Metadata["weight"]),
		fmt.Sprintf("INSTANCE_ID=%d", instanceID),
	}

	// Prepare the command
	cmd := exec.Command("go", "run", "./go-server/cmd/server.go")
	cmd.Env = append(os.Environ(), env...)

	// Capture stdout and stderr
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start server process: %v", err)
	}

	runningProcesses = append(runningProcesses, cmd.Process)
	return nil
}

// StartAllServers starts all configured server instances
func StartAllServers(config *Config) {
	var successCount, failureCount int
	var successedServers, failedServers []string

	defer func() {
		signal.Notify(shutdownChan, syscall.SIGINT, syscall.SIGTERM)
		<-shutdownChan
		gracefulShutdown()
	}()

	for _, group := range config.ServerGroups {
		for i := 0; i < group.InitInstances; i++ {
			serverID := fmt.Sprintf("%s-instance-%d on port %d", group.ModelName, i, group.Port+i)
			if err := startServer(group, i); err != nil {
				logger.Warnf("Failed to start server %s: %v", serverID, err)
				failureCount++
				failedServers = append(failedServers, serverID)
			} else {
				successCount++
				successedServers = append(successedServers, serverID)
			}
			// Add a small delay between starting instances
			time.Sleep(time.Second)
		}
	}

	// Wait for a few seconds to ensure logs are flushed
	time.Sleep(3 * time.Second)
	logger.Infof("========= Server startup summary =========")
	logger.Infof("Successfully started %d servers: \n%v", successCount, successedServers)
	if failureCount > 0 {
		logger.Warnf("Failed to start: %d servers", failureCount)
		logger.Warnf("Failed servers: %v", failedServers)
	}
}

func gracefulShutdown() {
	logger.Info("Initiating graceful shutdown...")

	var wg sync.WaitGroup

	shutdownTimeout := time.After(10 * time.Second)

	for _, process := range runningProcesses {
		wg.Add(1)
		go func(p *os.Process) {
			defer wg.Done()

			if err := p.Signal(syscall.SIGTERM); err != nil {
				logger.Warnf("Failed to send SIGTERM to process %d: %v", p.Pid, err)
				_ = p.Kill()
				return
			}

			done := make(chan error)
			go func() {
				_, err := p.Wait()
				done <- err
			}()

			select {
			case err := <-done:
				if err != nil {
					logger.Warnf("Process %d exited with error: %v", p.Pid, err)
				} else {
					// Wait for a few seconds to ensure logs are flushed
					time.Sleep(time.Second * 5)
					logger.Infof("Graceful shutdown --- Process %d shutdown successfully", p.Pid)
				}
			case <-shutdownTimeout:
				logger.Warnf("Process %d shutdown timed out, forcing kill", p.Pid)
				_ = p.Kill()
			}
		}(process)
	}

	wg.Wait()
	logger.Info("All processes have been graceful shutdown")
}

func main() {
	cfg, err := LoadConfig("gateway.yaml")
	if err != nil {
		logger.Errorf("Failed to load config: %v", err)
		return
	}

	StartAllServers(cfg)
}
