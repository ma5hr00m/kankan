package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"kankan/pkg/crypto"
	"kankan/pkg/protocol"
)

type Config struct {
	BindAddr          string
	BindPort          int
	Key               string
	ProxyPort         int
	MaxConnections    int
	ConnTimeout       time.Duration
	HeartbeatInterval time.Duration
	BufferSize        int
	CleanupInterval   time.Duration
	LogLevel          string
	IdleTimeout       time.Duration
	EnableUDP         bool
	UDPBufferSize     int
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
	flag.BoolVar(&config.EnableUDP, "enable-udp", false, "Enable UDP proxy support")
	flag.IntVar(&config.UDPBufferSize, "udp-buffer", 65507, "UDP buffer size (bytes)")
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
	config      Config
	listener    net.Listener
	proxy       net.Listener
	udpProxy    *net.UDPConn
	clients     map[string]*Client
	udpSessions map[string]*UDPSession
	mu          sync.RWMutex
	activeConns int32
	workerPool  *WorkerPool
	bufferPool  sync.Pool
	metrics     *Metrics
}

type WorkerPool struct {
	workers chan struct{}
}

func NewWorkerPool(size int) *WorkerPool {
	return &WorkerPool{
		workers: make(chan struct{}, size),
	}
}

func (p *WorkerPool) Submit(task func()) {
	p.workers <- struct{}{}
	go func() {
		defer func() { <-p.workers }()
		task()
	}()
}

type UDPSession struct {
	clientAddr *net.UDPAddr
	localConn  *net.UDPConn
	lastSeen   time.Time
	crypto     *crypto.Crypto
}

type Metrics struct {
	activeConnections int32
	totalConnections  uint64
	totalBytes        uint64
	totalPackets      uint64
	totalErrors       uint64
	lastMinuteBytes   uint64
	lastMinutePackets uint64
	lastUpdateTime    time.Time
	mu                sync.RWMutex
}

func (m *Metrics) recordBytes(n int) {
	atomic.AddUint64(&m.totalBytes, uint64(n))
	atomic.AddUint64(&m.lastMinuteBytes, uint64(n))
}

func (m *Metrics) recordPacket() {
	atomic.AddUint64(&m.totalPackets, 1)
	atomic.AddUint64(&m.lastMinutePackets, 1)
}

func (m *Metrics) recordError() {
	atomic.AddUint64(&m.totalErrors, 1)
}

func (m *Metrics) updateStats() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if now.Sub(m.lastUpdateTime) >= time.Minute {
		atomic.StoreUint64(&m.lastMinuteBytes, 0)
		atomic.StoreUint64(&m.lastMinutePackets, 0)
		m.lastUpdateTime = now
	}
}

func NewServer(config Config) *Server {
	return &Server{
		config:      config,
		clients:     make(map[string]*Client),
		udpSessions: make(map[string]*UDPSession),
		activeConns: 0,
		workerPool:  NewWorkerPool(config.MaxConnections),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, config.BufferSize)
			},
		},
		metrics: &Metrics{
			lastUpdateTime: time.Now(),
		},
	}
}

func (s *Server) getBuf(size int) []byte {
	buf := s.bufferPool.Get().([]byte)
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}

func (s *Server) putBuf(buf []byte) {
	if cap(buf) <= s.config.BufferSize {
		s.bufferPool.Put(buf[:0])
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
		return fmt.Errorf("failed to start control server: %w", err)
	}

	if err := s.startProxyServer(); err != nil {
		return fmt.Errorf("failed to start proxy server: %w", err)
	}

	if s.config.EnableUDP {
		if err := s.startUDPProxy(); err != nil {
			return fmt.Errorf("failed to start UDP proxy: %w", err)
		}
	}

	go s.cleanInactiveClients()
	go s.updateMetrics()

	return nil
}

func (s *Server) updateMetrics() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.metrics.updateStats()
		activeConns := atomic.LoadInt32(&s.activeConns)
		atomic.StoreInt32(&s.metrics.activeConnections, activeConns)
	}
}

func (s *Server) startControlServer() error {
	addr := fmt.Sprintf("%s:%d", s.config.BindAddr, s.config.BindPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start control server: %w", err)
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
		return fmt.Errorf("failed to start proxy server: %w", err)
	}
	s.proxy = listener

	log.Printf("[INFO] Proxy server listening on %s\n", addr)

	go s.acceptProxyConnections()
	return nil
}

func (s *Server) startUDPProxy() error {
	addr := fmt.Sprintf("%s:%d", s.config.BindAddr, s.config.ProxyPort)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	s.udpProxy, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	go s.handleUDPProxy()
	log.Printf("[INFO] UDP proxy listening on %s\n", addr)
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
	defer atomic.AddInt32(&s.activeConns, -1)

	clientAddr := conn.RemoteAddr().String()
	log.Printf("[INFO] New control connection from %s\n", clientAddr)

	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Printf("[ERR] Failed to read auth message: %v\n", err)
		return
	}
	defer protocol.ReleaseMessage(msg)

	if msg.Type != protocol.MsgTypeAuth {
		log.Printf("[ERR] First message must be auth\n")
		return
	}

	crypto, err := crypto.NewCrypto(s.config.Key)
	if err != nil {
		log.Printf("[ERR] Failed to create crypto: %v\n", err)
		return
	}

	client := &Client{
		conn:     conn,
		crypto:   crypto,
		lastSeen: time.Now(),
	}

	s.mu.Lock()
	s.clients[clientAddr] = client
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, clientAddr)
		s.mu.Unlock()
	}()

	for {
		msg, err := protocol.ReadMessage(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[ERR] Failed to read message: %v\n", err)
			}
			return
		}

		s.workerPool.Submit(func() {
			defer protocol.ReleaseMessage(msg)
			if err := s.handleClientData(clientAddr, msg); err != nil {
				log.Printf("[ERR] Failed to handle client data: %v\n", err)
			}
		})

		client.lastSeen = time.Now()
	}
}

func (s *Server) handleProxyConnection(conn net.Conn) {
	defer conn.Close()
	defer atomic.AddInt32(&s.activeConns, -1)

	clientAddr := conn.RemoteAddr().String()
	s.mu.RLock()
	client, exists := s.clients[clientAddr]
	s.mu.RUnlock()

	if !exists {
		log.Printf("[ERR] Unknown client %s\n", clientAddr)
		return
	}

	localAddr := fmt.Sprintf("%s:%d", s.config.BindAddr, 0)
	local, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("[ERR] Failed to connect to local service: %v\n", err)
		return
	}
	defer local.Close()

	errChan := make(chan error, 2)

	s.workerPool.Submit(func() {
		errChan <- s.proxyData(local, conn, client)
	})

	s.workerPool.Submit(func() {
		errChan <- s.proxyData(conn, local, client)
	})

	<-errChan
}

func (s *Server) proxyData(dst net.Conn, src net.Conn, client *Client) error {
	buf := s.getBuf(s.config.BufferSize)
	defer s.putBuf(buf)

	for {
		n, err := src.Read(buf)
		if err != nil {
			s.metrics.recordError()
			return err
		}

		data := buf[:n]
		s.metrics.recordBytes(n)
		s.metrics.recordPacket()

		if src == client.conn {
			data, err = client.crypto.Decrypt(data)
			if err != nil {
				s.metrics.recordError()
				return err
			}
		} else {
			data, err = client.crypto.Encrypt(data)
			if err != nil {
				s.metrics.recordError()
				return err
			}
		}

		if _, err := dst.Write(data); err != nil {
			s.metrics.recordError()
			return err
		}
	}
}

func (s *Server) handleUDPProxy() {
	buffer := s.getBuf(s.config.UDPBufferSize)
	defer s.putBuf(buffer)

	for {
		n, remoteAddr, err := s.udpProxy.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("[ERR] Failed to read UDP data: %v\n", err)
			continue
		}

		data := buffer[:n]
		s.workerPool.Submit(func() {
			s.handleUDPData(remoteAddr, data)
		})
	}
}

func (s *Server) handleUDPData(remoteAddr *net.UDPAddr, data []byte) {
	s.mu.RLock()
	session, exists := s.udpSessions[remoteAddr.String()]
	s.mu.RUnlock()

	if !exists {
		return
	}

	session.lastSeen = time.Now()
	s.metrics.recordBytes(len(data))
	s.metrics.recordPacket()

	decrypted, err := session.crypto.Decrypt(data)
	if err != nil {
		s.metrics.recordError()
		log.Printf("[ERR] Failed to decrypt UDP data: %v\n", err)
		return
	}

	if _, err := session.localConn.Write(decrypted); err != nil {
		s.metrics.recordError()
		log.Printf("[ERR] Failed to write UDP data to local connection: %v\n", err)
	}
}

func (s *Server) handleClientData(clientAddr string, msg *protocol.Message) error {
	switch msg.Type {
	case protocol.MsgTypeHeartbeat:
		return nil
	case protocol.MsgTypeNewProxy:
		s.mu.RLock()
		client, exists := s.clients[clientAddr]
		s.mu.RUnlock()

		if !exists {
			return fmt.Errorf("client not found")
		}

		if len(msg.Payload) < 1 {
			return fmt.Errorf("invalid proxy request")
		}

		proxyType := msg.Payload[0]
		if proxyType == protocol.ProxyTypeUDP && s.config.EnableUDP {
			return s.handleUDPProxyRequest(clientAddr, client)
		}
		return nil
	default:
		return fmt.Errorf("unknown message type")
	}
}

func (s *Server) handleUDPProxyRequest(clientAddr string, client *Client) error {
	localAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", s.config.BindAddr, 0))
	if err != nil {
		return err
	}

	localConn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return err
	}

	remoteAddr, err := net.ResolveUDPAddr("udp", clientAddr)
	if err != nil {
		return err
	}

	session := &UDPSession{
		clientAddr: remoteAddr,
		localConn:  localConn,
		lastSeen:   time.Now(),
		crypto:     client.crypto,
	}

	s.mu.Lock()
	s.udpSessions[clientAddr] = session
	s.mu.Unlock()

	go s.handleUDPSession(session)
	return nil
}

func (s *Server) handleUDPSession(session *UDPSession) {
	buffer := s.getBuf(s.config.UDPBufferSize)
	defer s.putBuf(buffer)

	for {
		n, _, err := session.localConn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("[ERR] Failed to read from UDP session: %v\n", err)
			break
		}

		session.lastSeen = time.Now()

		encrypted, err := session.crypto.Encrypt(buffer[:n])
		if err != nil {
			s.metrics.recordError()
			log.Printf("[ERR] Failed to encrypt UDP data: %v\n", err)
			break
		}

		if _, err := s.udpProxy.WriteToUDP(encrypted, session.clientAddr); err != nil {
			s.metrics.recordError()
			log.Printf("[ERR] Failed to write to UDP proxy: %v\n", err)
			break
		}
	}

	s.mu.Lock()
	delete(s.udpSessions, session.clientAddr.String())
	s.mu.Unlock()
	session.localConn.Close()
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
