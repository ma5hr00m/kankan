# Kankan

[![Go Version](https://img.shields.io/badge/Go-1.23.5-blue.svg)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)

Kankan 是一个使用 Go 语言编写的轻量级安全内网穿透工具。

## 使用方法

### 服务端（公网）

```bash
# 基础用法
kankans.exe -bind 0.0.0.0 -port 8080 -proxy 8081 -key your-secret-key

# 高级配置
kankans.exe \
  -bind 0.0.0.0 \
  -port 8080 \
  -proxy 8081 \
  -key your-secret-key \
  -max-conn 100 \
  -timeout 30s \
  -heartbeat 10s \
  -buffer 4096
```

### 客户端（内网）

```bash
# 基础用法 - 暴露本地Web服务
kankanc.exe -server your-server-ip -sport 8080 -local 127.0.0.1 -lport 80 -key your-secret-key

# 高级配置
kankanc.exe \
  -server your-server-ip \
  -sport 8080 \
  -local 127.0.0.1 \
  -lport 80 \
  -key your-secret-key
```

## 命令行选项

### 服务端选项
- `-bind`：绑定地址（默认："0.0.0.0"）
- `-port`：控制服务器端口（默认：8080）
- `-proxy`：代理服务器端口（默认：8081）
- `-key`：加密密钥（必需）
- `-max-conn`：最大连接数（默认：1000）
- `-timeout`：连接超时时间（默认：30s）
- `-heartbeat`：心跳间隔（默认：10s）
- `-buffer`：缓冲区大小（字节）（默认：4096）
- `-cleanup`：清理间隔（默认：60s）
- `-log-level`：日志级别（debug/info/warn/error，默认：info）
- `-idle-timeout`：空闲连接超时（默认：300s）

### 客户端选项
- `-server`：服务器地址（默认："127.0.0.1"）
- `-sport`：服务器端口（默认：8080）
- `-local`：本地服务地址（默认："127.0.0.1"）
- `-lport`：本地服务端口（默认：80）
- `-key`：加密密钥（必需）
