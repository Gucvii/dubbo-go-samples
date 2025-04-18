/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/dubbogo/gost/log/logger"
	"github.com/joho/godotenv"
	"os"
	"strings"
)

import (
	"dubbo.apache.org/dubbo-go/v3"
	_ "dubbo.apache.org/dubbo-go/v3/imports"
	"dubbo.apache.org/dubbo-go/v3/registry"
)

import (
	chat "github.com/apache/dubbo-go-samples/llm/proto"
	gateway "github.com/apache/dubbo-go-samples/llm/proto1"
)

type ChatContext struct {
	ID      string
	History []*chat.ChatMessage
}

var (
	contexts        = make(map[string]*ChatContext)
	currentCtxID    string
	contextOrder    []string
	maxID           uint8 = 0
	availableModels []string
	currentModel    string
	maxContextCount int
)

func handleCommand(cmd string) (resp string) {
	cmd = strings.TrimSpace(cmd)
	resp = ""
	switch {
	case cmd == "/?" || cmd == "/help":
		resp += "Available commands:\n"
		resp += "/? help        - Show this help\n"
		resp += "/list          - List all contexts\n"
		resp += "/cd <context>  - Switch context\n"
		resp += "/new           - Create new context\n"
		resp += "/models        - List available models\n"
		resp += "/model <name>  - Switch to specified model"
		return resp
	case cmd == "/list":
		fmt.Printf("Stored contexts (max %d):\n", maxContextCount)
		for _, ctxID := range contextOrder {
			resp += fmt.Sprintf("- %s\n", ctxID)
		}
		resp = strings.TrimSuffix(resp, "\n")
		return resp
	case strings.HasPrefix(cmd, "/cd "):
		target := strings.TrimPrefix(cmd, "/cd ")
		if ctx, exists := contexts[target]; exists {
			currentCtxID = ctx.ID
			resp += fmt.Sprintf("Switched to context: %s", target)
		} else {
			resp += "Context not found"
		}
		return resp
	case cmd == "/new":
		newID := createContext()
		currentCtxID = newID
		resp += fmt.Sprintf("Created new context: %s", newID)
		return resp
	case cmd == "/models":
		resp += "Available models:"
		for _, model := range availableModels {
			marker := " "
			if model == currentModel {
				marker = "*"
			}
			resp += fmt.Sprintf("\n%s %s", marker, model)
		}
		return resp
	case strings.HasPrefix(cmd, "/model "):
		modelName := strings.TrimPrefix(cmd, "/model ")
		modelFound := false
		for _, model := range availableModels {
			if model == modelName {
				currentModel = model
				modelFound = true
				break
			}
		}
		if modelFound {
			resp += fmt.Sprintf("Switched to model: %s", modelName)
		} else {
			resp += fmt.Sprintf("Model '%s' not found. Use /models to see available models.", modelName)
		}
		return resp
	default:
		return "Invalid command, use /? for help"
	}
}

func createContext() string {
	id := fmt.Sprintf("ctx%d", maxID)
	maxID++
	contexts[id] = &ChatContext{
		ID:      id,
		History: []*chat.ChatMessage{},
	}
	contextOrder = append(contextOrder, id)

	// Use configurable max context count
	if len(contextOrder) > maxContextCount {
		delete(contexts, contextOrder[0])
		contextOrder = contextOrder[1:]
	}
	return id
}

func main() {
	maxContextCount = 3
	currentCtxID = createContext()

	// Local environment variables take precedence over .client.env file
	nacosUrl := os.Getenv("NACOS_URL")
	gatewayServiceName := os.Getenv("GATEWAY_SERVICE_NAME")
	modelServiceGroup := os.Getenv("MODEL_SERVICE_GROUP")

	err := godotenv.Load(".client.env")
	if err != nil {
		logger.Infof("Error loading .client.env file: %v\n", err)
		return
	}

	if nacosUrl == "" {
		logger.Info("NACOS_URL is not set, loading from .client.env file...")
		nacosUrl = os.Getenv("NACOS_URL")
		if nacosUrl == "" {
			logger.Info("NACOS_URL is also not set in .client.env file")
			return
		}
	}

	if gatewayServiceName == "" {
		logger.Info("GATEWAY_SERVICE_NAME is not set, loading from .client.env file...")
		gatewayServiceName = os.Getenv("GATEWAY_SERVICE_NAME")
		if gatewayServiceName == "" {
			logger.Info("GATEWAY_SERVICE_NAME is also not set in .client.env file")
			return
		}
	}

	if modelServiceGroup == "" {
		logger.Info("MODEL_SERVICE_GROUP is not set, loading from .client.env file...")
		modelServiceGroup = os.Getenv("MODEL_SERVICE_GROUP")
		if modelServiceGroup == "" {
			logger.Info("MODEL_SERVICE_GROUP is also not set in .client.env file")
			return
		}
	}

	// Create Dubbo instance for gateway service
	ins, err := dubbo.NewInstance(
		dubbo.WithName(gatewayServiceName),
		dubbo.WithRegistry(
			registry.WithNacos(),
			registry.WithAddress(nacosUrl),
		),
	)
	if err != nil {
		logger.Errorf("Error creating dubbo instance for gateway service: %v\n", err)
		return
	}

	cli, err := ins.NewClient()
	if err != nil {
		logger.Errorf("Error creating dubbo client: %v\n", err)
		return
	}

	svc0, err := gateway.NewGatewayService(cli)
	if err != nil {
		fmt.Printf("Error creating gateway service: %v\n", err)
		return
	}

	info, err := svc0.GetInfo(context.Background(),
		&gateway.GetInfoRequest{
			GroupName: modelServiceGroup,
		})
	if err != nil {
		fmt.Printf("Error getting model info: %v\n", err)
		return
	}

	availableModels = info.AvailableModels
	if len(availableModels) == 0 {
		fmt.Println("No available models found.")
		return
	}
	currentModel = availableModels[0]

	svc, err := chat.NewChatService(cli)
	if err != nil {
		fmt.Printf("Error creating service: %v\n", err)
		return
	}

	fmt.Printf("\nSend a message (/? for help) - Using model: %s\n", currentModel)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		scanner.Scan()
		input := scanner.Text()

		// handle command
		if strings.HasPrefix(input, "/") {
			fmt.Println(handleCommand(input))
			continue
		}

		func() {
			currentCtx := contexts[currentCtxID]
			currentCtx.History = append(currentCtx.History,
				&chat.ChatMessage{
					Role:    "human",
					Content: input,
					Bin:     nil,
				})

			stream, err := svc.Chat(context.Background(), &chat.ChatRequest{
				Messages: currentCtx.History,
				Model:    currentModel,
			})
			if err != nil {
				panic(err)
			}
			defer func(stream chat.ChatService_ChatClient) {
				err := stream.Close()
				if err != nil {
					fmt.Printf("Error closing stream: %v\n", err)
				}
			}(stream)

			resp := ""

			for stream.Recv() {
				msg := stream.Msg()
				c := msg.Content
				resp += c
				fmt.Print(c)
			}
			fmt.Print("\n")

			if err := stream.Err(); err != nil {
				fmt.Printf("Stream error: %v\n", err)
				return
			}

			currentCtx.History = append(currentCtx.History,
				&chat.ChatMessage{
					Role:    "ai",
					Content: resp,
					Bin:     nil,
				})
		}()
	}
}
