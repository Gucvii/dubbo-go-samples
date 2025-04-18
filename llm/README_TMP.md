本机安装nacos，并暴露8848端口,9848端口

安装ollama，然后下载模型

```shell
ollama pull llava:7b
ollama pull qwen2.5:7b
ollama pull qwen2.5:3b
```

确保你本机的11435端口映射到了11434，如果你在远程主机安装的ollama，请把11434和11435映射到远程

然后在 llm 目录下执行以下命令

```shell
go run ./go-server/cmd/startservers.go #启动多台server
go run ./go-gateway/cmd #启动网关
go run ./go-client/cmd #启动client
```
测试你就会发现那个issue