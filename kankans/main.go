package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"kankan/pkg/crypto"
	"kankan/pkg/protocol"
)

type Config struct {
	BindAddr            string
	BindPort            int
	Key                 string
	ProxyPort           int
	MaxConnections      int
	ConnTimeout         time.Duration
	HeartbeatInterval   time.Duration
	BufferSize          int
	CleanupInterval     time.Duration
	LogLevel            string
	IdleTimeout         time.Duration
}

var config Config

func init() {
	flag.StringVar(&config.BindAddr, "bind", "0.0.0.0", "Bind address")
	flag.IntVar(&config.BindPort, "port", 8080, "Control server port")
	flag.StringVar(&config.Key, "key", "", "Encryption key")
	flag.IntVar(&config.ProxyPort, "proxy", 8081, "Proxy server port")
	flag.IntVar(&config.MaxConnections, "max-conn", 1000, "Maximum connections")
	flag.DurationVar(&config.ConnTimeout, "timeout", 30*time.Second, "Connection timeout")
	flag.DurationVar(&config.HeartbeatInterval, "heartbeat", 10*time.Second, "Heartbeat interval")
	flag.IntVar(&config.BufferSize, "buffer", 4096, "Buffer size (bytes)")
	flag.DurationVar(&config.CleanupInterval, "cleanup", 60*time.Second, "Cleanup interval")
	flag.StringVar(&config.LogLevel, "log-level", "info", "Log level (debug/info/warn/error)")
	flag.DurationVar(&config.IdleTimeout, "idle-timeout", 300*time.Second, "Idle connection timeout")
}

func main() {
	flag.Parse()
	
	if config.Key == "" {
		log.Fatal("[ERR] Encryption key is required")
	}

	server := NewServer(config)
	if err := server.Start(); err != nil {
		log.Fatal("[ERR] Server failed to start:", err)
	}
}

type Client struct {
	conn     net.Conn
	crypto   *crypto.Crypto
	lastSeen time.Time
}

type Server struct {
	config         Config
	listener       net.Listener
	proxy          net.Listener
	clients        map[string]*Client
	mu             sync.RWMutex
	activeConns    int32
}

func NewServer(config Config) *Server {
	return &Server{
		config:      config,
		clients:     make(map[string]*Client),
		activeConns: 0,
	}
}

func (s *Server) Start() error {
	if s.config.LogLevel != "" {
		switch s.config.LogLevel {
		case "debug":
			log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Llongfile)
		case "info":
			log.SetFlags(log.Ldate | log.Ltime)
		case "warn", "error":
			log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
		}
	}

	if err := s.startControlServer(); err != nil {
		return fmt.Errorf("[ERR] Failed to start control server: %w", err)
	}

	if err := s.startProxyServer(); err != nil {
		return fmt.Errorf("[ERR] Failed to start proxy server: %w", err)
	}

	go s.cleanInactiveClients()
	
	log.Printf("[INFO] Server started successfully on %s:%d (control) and :%d (proxy)",
		s.config.BindAddr, s.config.BindPort, s.config.ProxyPort)
	
	select {}
}

func (s *Server) startControlServer() error {
	addr := fmt.Sprintf("%s:%d", s.config.BindAddr, s.config.BindPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("[ERR] Failed to start control server: %w", err)
	}
	s.listener = listener

	log.Printf("[INFO] Control server listening on %s\n", addr)

	go s.acceptControlConnections()
	return nil
}

func (s *Server) startProxyServer() error {
	addr := fmt.Sprintf("%s:%d", s.config.BindAddr, s.config.ProxyPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("[ERR] Failed to start proxy server: %w", err)
	}
	s.proxy = listener

	log.Printf("[INFO] Proxy server listening on %s\n", addr)

	go s.acceptProxyConnections()
	return nil
}

func (s *Server) acceptControlConnections() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			log.Printf("[ERR] Failed to accept control connection: %v\n", err)
			continue
		}

		go s.handleControlConnection(conn)
	}
}

func (s *Server) acceptProxyConnections() {
	for {
		conn, err := s.proxy.Accept()
		if err != nil {
			log.Printf("[ERR] Failed to accept proxy connection: %v\n", err)
			continue
		}

		go s.handleProxyConnection(conn)
	}
}

func (s *Server) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	
	if atomic.LoadInt32(&s.activeConns) >= int32(s.config.MaxConnections) {
		log.Printf("[ERR] Maximum connections reached: %d", s.config.MaxConnections)
		return
	}
	
	atomic.AddInt32(&s.activeConns, 1)
	defer atomic.AddInt32(&s.activeConns, -1)

	conn.SetDeadline(time.Now().Add(s.config.ConnTimeout))
	
	crypto, err := crypto.NewCrypto(s.config.Key)
	if err != nil {
		log.Printf("[ERR] Failed to create crypto instance: %v\n", err)
		return
	}
	client := &Client{
		conn:     conn,
		crypto:   crypto,
		lastSeen: time.Now(),
	}

	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Printf("[ERR] Failed to read authentication message: %v\n", err)
		return
	}

	if msg.Type != protocol.MsgTypeAuth {
		log.Printf("[ERR] Unexpected message type: %d", msg.Type)
		return
	}

	clientAddr := conn.RemoteAddr().String()
	s.mu.Lock()
	s.clients[clientAddr] = client
	s.mu.Unlock()

	log.Printf("[INFO] Client authenticated: %s\n", clientAddr)

	for {
		msg, err := protocol.ReadMessage(conn)
		if err != nil {
			break
		}

		switch msg.Type {
		case protocol.MsgTypeHeartbeat:
			s.mu.Lock()
			if client, ok := s.clients[clientAddr]; ok {
				client.lastSeen = time.Now()
			}
			s.mu.Unlock()
		case protocol.MsgTypeData:
			s.handleClientData(clientAddr, msg)
		}
	}

	s.mu.Lock()
	delete(s.clients, clientAddr)
	s.mu.Unlock()
	log.Printf("[INFO] Client disconnected: %s\n", clientAddr)
}

func (s *Server) handleProxyConnection(conn net.Conn) {
	defer conn.Close()

	s.mu.RLock()
	if len(s.clients) == 0 {
		s.mu.RUnlock()
		log.Printf("[ERR] No available clients\n")
		return
	}

	var client *Client
	for _, c := range s.clients {
		client = c
		break
	}
	s.mu.RUnlock()

	msg := &protocol.Message{
		Type: protocol.MsgTypeNewProxy,
	}
	if err := protocol.WriteMessage(client.conn, msg); err != nil {
		log.Printf("[ERR] Failed to notify client of new proxy connection: %v\n", err)
		return
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}

			data, err := client.crypto.Encrypt(buf[:n])
			if err != nil {
				log.Printf("[ERR] Failed to encrypt data: %v\n", err)
				return
			}

			msg := &protocol.Message{
				Type:    protocol.MsgTypeData,
				Length:  uint32(len(data)),
				Payload: data,
			}
			if err := protocol.WriteMessage(client.conn, msg); err != nil {
				return
			}
		}
	}()

	for {
		msg, err := protocol.ReadMessage(client.conn)
		if err != nil {
			return
		}

		if msg.Type != protocol.MsgTypeData {
			continue
		}

		data, err := client.crypto.Decrypt(msg.Payload)
		if err != nil {
			log.Printf("[ERR] Failed to decrypt data: %v\n", err)
			return
		}

		if _, err := conn.Write(data); err != nil {
			return
		}
	}
}

func (s *Server) handleClientData(clientAddr string, msg *protocol.Message) {
	s.mu.RLock()
	client, ok := s.clients[clientAddr]
	s.mu.RUnlock()

	if !ok {
		return
	}

	data, err := client.crypto.Decrypt(msg.Payload)
	if err != nil {
		log.Printf("[ERR] Failed to decrypt data: %v\n", err)
		return
	}

	log.Printf("[INFO] Received data from %s: %d bytes\n", clientAddr, len(data))
}

func (s *Server) cleanInactiveClients() {
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for addr, client := range s.clients {
			if now.Sub(client.lastSeen) > s.config.IdleTimeout {
				client.conn.Close()
				delete(s.clients, addr)
				log.Printf("[INFO] Cleaned up inactive client: %s\n", addr)
			}
		}
		s.mu.Unlock()
	}
}
