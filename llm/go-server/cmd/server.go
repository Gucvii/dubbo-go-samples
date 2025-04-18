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
	"context"
	"encoding/base64"
	"fmt"
	"github.com/joho/godotenv"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
)

import (
	"dubbo.apache.org/dubbo-go/v3"
	_ "dubbo.apache.org/dubbo-go/v3/imports"
	"dubbo.apache.org/dubbo-go/v3/protocol"
	"dubbo.apache.org/dubbo-go/v3/registry"
	"github.com/dubbogo/gost/log/logger"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

import (
	chat "github.com/apache/dubbo-go-samples/llm/proto"
)

type ChatServer struct {
	llm       *ollama.LLM
	modelName string
	port      int
	weight    int64
}

func NewChatServer(modelName, url, provider string, port int, weight int64) (*ChatServer, error) {
	// Currently only support ollama
	if provider != "ollama" {
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}

	llm, err := ollama.New(
		ollama.WithModel(modelName),
		ollama.WithServerURL(url),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize model %s: %v", modelName, err)
	}

	return &ChatServer{
		llm:       llm,
		modelName: modelName,
		port:      port,
		weight:    weight,
	}, nil
}

func (s *ChatServer) Chat(ctx context.Context, req *chat.ChatRequest, stream chat.ChatService_ChatServer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("panic in Chat: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("internal server error")
		}
	}()
	// #test
	logger.Infof("Now server with weight %d on port %d answering your request.\n", s.weight, s.port)

	if s.modelName != req.Model {
		logger.Errorf("Model mismatch: expected %s, got %s\n", s.modelName, req.Model)
		return fmt.Errorf("model mismatch: expected %s, got %s", s.modelName, req.Model)
	}
	if s.llm == nil {
		logger.Error("LLM is not initialized")
		return fmt.Errorf("LLM is not initialized")
	}

	if len(req.Messages) == 0 {
		logger.Error("Request contains no messages")
		return fmt.Errorf("empty messages in request")
	}

	var messages []llms.MessageContent
	for _, msg := range req.Messages {
		var msgType llms.ChatMessageType
		switch msg.Role {
		case "human":
			msgType = llms.ChatMessageTypeHuman
		case "ai":
			msgType = llms.ChatMessageTypeAI
		case "system":
			msgType = llms.ChatMessageTypeSystem
		}

		messageContent := llms.MessageContent{
			Role: msgType,
			Parts: []llms.ContentPart{
				llms.TextContent{Text: msg.Content},
			},
		}

		if msg.Bin != nil && len(msg.Bin) != 0 {
			decodeByte, err := base64.StdEncoding.DecodeString(string(msg.Bin))
			if err != nil {
				logger.Errorf("Failed to decode base64 content: %v\n", err)
				return fmt.Errorf("failed to decode base64 content: %v", err)
			}
			imgType := http.DetectContentType(decodeByte)
			messageContent.Parts = append(messageContent.Parts, llms.BinaryPart(imgType, decodeByte))
		}

		messages = append(messages, messageContent)
	}

	_, err = s.llm.GenerateContent(
		ctx,
		messages,
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			if chunk == nil {
				return nil
			}
			return stream.Send(&chat.ChatResponse{
				Content: string(chunk),
				Model:   s.modelName,
			})
		}),
	)
	if err != nil {
		logger.Errorf("GenerateContent failed with model %s: %v\n", s.modelName, err)
		return fmt.Errorf("GenerateContent failed with model %s: %v", s.modelName, err)
	}

	return nil
}

func main() {
	// Local environment variables take precedence over .env file
	modelName := os.Getenv("OLLAMA_MODEL")
	ollamaUrl := os.Getenv("OLLAMA_URL")
	provider := os.Getenv("PROVIDER")
	servicePort := os.Getenv("SERVICE_PORT")
	nacosUrl := os.Getenv("NACOS_URL")
	weight := os.Getenv("WEIGHT")

	err := godotenv.Load(".env")
	if err != nil {
		logger.Infof("Error loading .env file: %v\n", err)
		return
	}

	if modelName == "" {
		logger.Info("OLLAMA_MODEL is not set, loading from .env file...")
		modelName = os.Getenv("OLLAMA_MODEL")
		if modelName == "" {
			logger.Info("OLLAMA_MODEL is also not set in .env file")
			return
		}
	}

	if ollamaUrl == "" {
		logger.Info("OLLAMA_URL is not set, loading from .env file...")
		ollamaUrl = os.Getenv("OLLAMA_URL")
		if ollamaUrl == "" {
			logger.Info("OLLAMA_URL is also not set in .env file")
			return
		}
	}

	if provider == "" {
		logger.Info("PROVIDER is not set, loading from .env file...")
		provider = os.Getenv("PROVIDER")
		if provider == "" {
			logger.Info("PROVIDER is also not set in .env file")
			return
		}
	}

	port := 0
	if servicePort != "" {
		var err error
		port, err = strconv.Atoi(servicePort)
		if err != nil {
			logger.Infof("Invalid port number from environment: %v\n", err)
			return
		}
	} else {
		logger.Info("SERVICE_PORT is not set, loading from .env file...")
		servicePort = os.Getenv("SERVICE_PORT")
		if servicePort == "" {
			logger.Info("SERVICE_PORT is also not set in .env file")
			return
		}
		var err error
		port, err = strconv.Atoi(servicePort)
		if err != nil {
			logger.Infof("Invalid port number from .env file: %v\n", err)
			return
		}
	}

	if nacosUrl == "" {
		logger.Info("NACOS_URL is not set, loading from .env file...")
		nacosUrl = os.Getenv("NACOS_URL")
		if nacosUrl == "" {
			logger.Info("NACOS_URL is also not set in .env file")
			return
		}
	}

	var weightInt int64
	if weight == "" {
		logger.Info("WEIGHT is not set, loading from .env file...")
		weight = os.Getenv("WEIGHT")
		if weight == "" {
			// In the source code of dubbogo, if weight is not specified, the initial value should be 0
			logger.Info("WEIGHT is also not set in .env file, " +
				"the cluster load balancing strategy will keep equal weight")
			weight = "0"
		}
	}

	weightInt, err = strconv.ParseInt(weight, 10, 64)
	if err != nil {
		logger.Infof("Invalid WEIGHT: %v\n", err)
		return
	}

	ins, err := dubbo.NewInstance(
		dubbo.WithName(modelName),
		dubbo.WithGroup("MODEL_GROUP"),
		dubbo.WithRegistry(
			registry.WithNacos(),
			registry.WithAddress(nacosUrl),
			registry.WithWeight(weightInt),
			registry.WithGroup("MODEL_GROUP"),
		),
		dubbo.WithProtocol(
			protocol.WithTriple(),
			protocol.WithPort(port),
		),
	)

	srv, err := ins.NewServer()
	if err != nil {
		logger.Errorf("Error creating server: %v\n", err)
		return
	}

	chatServer, err := NewChatServer(modelName, ollamaUrl, provider, port, weightInt)
	if err != nil {
		logger.Errorf("Error creating chat server: %v\n", err)
		return
	}

	// Register the chat server
	if err := chat.RegisterChatServiceHandler(srv, chatServer); err != nil {
		logger.Errorf("Error registering handler: %v\n", err)
		return
	}

	if err := srv.Serve(); err != nil {
		logger.Errorf("Error starting server: %v\n", err)
		return
	}
}
