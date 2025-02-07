# Kankan

[![Go Version](https://img.shields.io/badge/Go-1.23.5-blue.svg)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)

Kankan is a lightweight and secure intranet penetration tool written in Go.

## Usage

### Server (Public Network)

```bash
# Basic usage
kankans.exe -bind 0.0.0.0 -port 8080 -proxy 8081 -key your-secret-key

# Advanced configuration
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

### Client (Internal Network)

```bash
# Basic usage - Expose local web service
kankanc.exe -server your-server-ip -sport 8080 -local 127.0.0.1 -lport 80 -key your-secret-key

# Advanced configuration
kankanc.exe \
  -server your-server-ip \
  -sport 8080 \
  -local 127.0.0.1 \
  -lport 80 \
  -key your-secret-key
```

## Command Line Options

### Server Options
- `-bind`: Bind address (default: "0.0.0.0")
- `-port`: Control server port (default: 8080)
- `-proxy`: Proxy server port (default: 8081)
- `-key`: Encryption key (required)
- `-max-conn`: Maximum connections (default: 1000)
- `-timeout`: Connection timeout (default: 30s)
- `-heartbeat`: Heartbeat interval (default: 10s)
- `-buffer`: Buffer size in bytes (default: 4096)
- `-cleanup`: Cleanup interval (default: 60s)
- `-log-level`: Log level (debug/info/warn/error, default: info)
- `-idle-timeout`: Idle connection timeout (default: 300s)

### Client Options
- `-server`: Server address (default: "127.0.0.1")
- `-sport`: Server port (default: 8080)
- `-local`: Local service address (default: "127.0.0.1")
- `-lport`: Local service port (default: 80)
- `-key`: Encryption key (required)