package main

import (
	"context"
	"dubbo.apache.org/dubbo-go/v3"
	"dubbo.apache.org/dubbo-go/v3/client"
	_ "dubbo.apache.org/dubbo-go/v3/imports"
	"dubbo.apache.org/dubbo-go/v3/protocol"
	"dubbo.apache.org/dubbo-go/v3/registry"
	"fmt"
	chat "github.com/apache/dubbo-go-samples/llm/proto"
	gateway "github.com/apache/dubbo-go-samples/llm/proto1"
	"github.com/dubbogo/gost/log/logger"
	"github.com/nacos-group/nacos-sdk-go/clients"
	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/nacos-group/nacos-sdk-go/vo"
	"gopkg.in/yaml.v3"
	"os"
	"strconv"
	"strings"
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

func getNacosAllServiceName(nacosUrl, groupName string) ([]string, error) {
	ipAddr := strings.Split(nacosUrl, ":")[0]
	port, err := strconv.ParseUint(strings.Split(nacosUrl, ":")[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Nacos URL: %v", err)
	}
	sc := []constant.ServerConfig{
		{
			IpAddr: ipAddr,
			Port:   port,
		},
	}

	cc := constant.ClientConfig{
		NamespaceId:         "",
		TimeoutMs:           5000,
		NotLoadCacheAtStart: true,
	}

	namingClient, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  &cc,
			ServerConfigs: sc,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(fmt.Sprintf("Failed to get naming client: %v", err))
	}

	param := vo.GetAllServiceInfoParam{GroupName: groupName}
	services, err := namingClient.GetAllServicesInfo(param)
	if err != nil {
		return nil, fmt.Errorf(fmt.Sprintf("Failed to get all services info: %v", err))
	}

	return services.Doms, nil
}

func getModelClientMap(nacosUrl, groupName string) (map[string]*client.Client, error) {
	modelClientMap := make(map[string]*client.Client)

	services, err := getNacosAllServiceName(nacosUrl, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to get nacos services: %w", err)
	}

	if len(services) == 0 {
		return nil, fmt.Errorf("no services found in nacos")
	}

	logger.Infof("Found %d services in nacos group %s", len(services), groupName)

	for _, serviceName := range services {
		logger.Infof("Creating dubbo client for service: %s", serviceName)

		ins, err := dubbo.NewInstance(
			dubbo.WithName(serviceName),
			dubbo.WithGroup(groupName),
			dubbo.WithRegistry(
				registry.WithNacos(),
				registry.WithAddress(nacosUrl),
				registry.WithGroup(groupName),
			),
		)

		if err != nil {
			return nil, fmt.Errorf("failed to create dubbo instance for %s: %w", serviceName, err)
		}

		cli, err := ins.NewClient(
			client.WithClientLoadBalanceRoundRobin(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create client for %s: %w", serviceName, err)
		}

		modelClientMap[serviceName] = cli
		logger.Infof("Successfully created client for service: %s", serviceName)
	}

	return modelClientMap, nil
}

type Router struct {
	modelClientMap map[string]*client.Client
}

func (r Router) Chat(ctx context.Context, request *chat.ChatRequest, server chat.ChatService_ChatServer) error {
	if request == nil {
		return fmt.Errorf("request is nil")
	}

	if request.Model == "" {
		return fmt.Errorf("model name is empty")
	}

	c, ok := r.modelClientMap[request.Model]
	if !ok {
		return fmt.Errorf("model %s not found", request.Model)
	}

	svc, err := chat.NewChatService(c)
	if err != nil {
		return fmt.Errorf("failed to create chat service: %w", err)
	}

	// 获取流式响应
	stream, err := svc.Chat(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to call chat service: %w", err)
	}
	defer stream.Close()

	// 转发流式响应
	for stream.Recv() {
		msg := stream.Msg()
		if err := server.Send(msg); err != nil {
			return fmt.Errorf("failed to send response: %w", err)
		}
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("stream error: %w", err)
	}

	return nil
}

func NewRouter(nacosUrl, groupName string) *Router {
	modelClientMap, err := getModelClientMap(nacosUrl, groupName)
	if err != nil {
		logger.Errorf("Failed to get model-client map: %v", err)
		return nil
	}
	return &Router{modelClientMap}
}

type GatewayService struct {
	modelClientMap map[string]*client.Client
	config         *Config
}

func (s *GatewayService) GetInfo(ctx context.Context, request *gateway.GetInfoRequest) (*gateway.GetInfoResponse, error) {
	services, err := getNacosAllServiceName(s.config.NacosConfig.ServerAddr, request.GroupName)
	if err != nil {
		return nil, fmt.Errorf("failed to get services: %v", err)
	}

	return &gateway.GetInfoResponse{
		AvailableModels: services,
	}, nil
}

func main() {
	cfg, err := LoadConfig("gateway.yaml")
	if err != nil {
		logger.Errorf("Failed to load config: %v", err)
		return
	}

	ins, err := dubbo.NewInstance(
		dubbo.WithName(cfg.Global.GatewayServiceName),
		dubbo.WithRegistry(
			registry.WithNacos(),
			registry.WithAddress(cfg.NacosConfig.ServerAddr),
		),
		dubbo.WithProtocol(
			protocol.WithTriple(),
			protocol.WithPort(cfg.Global.Port),
		),
	)

	srv, err := ins.NewServer()
	if err != nil {
		logger.Errorf("Error creating server: %v\n", err)
		return
	}

	router := NewRouter(cfg.NacosConfig.ServerAddr, cfg.Global.ModelServiceGroup)
	gatewayService := &GatewayService{
		modelClientMap: router.modelClientMap,
		config:         cfg,
	}

	// Register both services
	if err := chat.RegisterChatServiceHandler(srv, router); err != nil {
		logger.Errorf("Error registering chat handler: %v\n", err)
		return
	}

	if err := gateway.RegisterGatewayServiceHandler(srv, gatewayService); err != nil {
		logger.Errorf("Error registering gateway handler: %v\n", err)
		return
	}

	if err := srv.Serve(); err != nil {
		logger.Errorf("Error starting server: %v\n", err)
		return
	}
}
