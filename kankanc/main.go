package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"kankan/pkg/crypto"
	"kankan/pkg/protocol"
)

type Config struct {
	ServerAddr string
	ServerPort int
	LocalAddr  string
	LocalPort  int
	Key        string
	UseUDP     bool
}

var config Config

func init() {
	flag.StringVar(&config.ServerAddr, "server", "127.0.0.1", "Server address")
	flag.IntVar(&config.ServerPort, "sport", 8080, "Server port")
	flag.StringVar(&config.LocalAddr, "local", "127.0.0.1", "Local service address")
	flag.IntVar(&config.LocalPort, "lport", 80, "Local service port")
	flag.StringVar(&config.Key, "key", "", "Encryption key")
	flag.BoolVar(&config.UseUDP, "udp", false, "Use UDP protocol for tunneling")
}

func main() {
	flag.Parse()

	if config.Key == "" {
		log.Fatal("[ERR] Encryption key is required")
	}

	client := NewClient(config)
	if err := client.Start(); err != nil {
		log.Fatal("[ERR] Client failed to start:", err)
	}
}

type Client struct {
	config         Config
	conn           net.Conn
	crypto         *crypto.Crypto
	retryCount     int
	sessionID      string
	mu             sync.RWMutex
	reconnectDelay time.Duration
	maxRetries     int
	isReconnecting bool
	bufferPool     sync.Pool
}

func NewClient(config Config) *Client {
	return &Client{
		config:         config,
		reconnectDelay: time.Second,
		maxRetries:     5,
		sessionID:      generateSessionID(),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 4096)
			},
		},
	}
}

func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Client) Start() error {
	for {
		err := c.connect()
		if err == nil {
			break
		}

		c.retryCount++
		if c.retryCount > c.maxRetries {
			return fmt.Errorf("max retries exceeded: %w", err)
		}

		time.Sleep(c.reconnectDelay)
		c.reconnectDelay *= 2
	}

	go c.monitorConnection()
	go c.handleConnection()
	return nil
}

func (c *Client) monitorConnection() {
	for {
		if c.conn == nil {
			return
		}

		_, err := c.conn.Write([]byte{0})
		if err != nil {
			c.handleDisconnect()
		}

		time.Sleep(time.Second * 5)
	}
}

func (c *Client) handleDisconnect() {
	c.mu.Lock()
	if c.isReconnecting {
		c.mu.Unlock()
		return
	}
	c.isReconnecting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.isReconnecting = false
		c.mu.Unlock()
	}()

	for i := 0; i < c.maxRetries; i++ {
		err := c.connect()
		if err == nil {
			c.retryCount = 0
			c.reconnectDelay = time.Second
			return
		}

		time.Sleep(c.reconnectDelay)
		c.reconnectDelay *= 2
		if c.reconnectDelay > time.Minute {
			c.reconnectDelay = time.Minute
		}
	}
}

func (c *Client) connect() error {
	addr := fmt.Sprintf("%s:%d", c.config.ServerAddr, c.config.ServerPort)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}

	var err1 error
	c.crypto, err1 = crypto.NewCrypto(c.config.Key)
	if err1 != nil {
		return err1
	}
	c.conn = conn

	msg := &protocol.Message{
		Type:    protocol.MsgTypeAuth,
		Payload: []byte(c.sessionID),
	}

	if err := protocol.WriteMessage(c.conn, msg); err != nil {
		c.conn.Close()
		return err
	}

	resp, err := protocol.ReadMessage(c.conn)
	if err != nil {
		c.conn.Close()
		return err
	}

	if resp.Type != protocol.MsgTypeAuthOK {
		c.conn.Close()
		return fmt.Errorf("authentication failed")
	}

	return nil
}

func (c *Client) handleConnection() {
	errChan := make(chan error, 2)
	go c.heartbeat(errChan)
	go c.handleProxy(errChan)

	err := <-errChan
	if err != nil {
		log.Printf("[ERR] Connection error: %v\n", err)
	}

	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
}

func (c *Client) heartbeat(errChan chan<- error) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		msg := protocol.AcquireMessage()
		msg.Type = protocol.MsgTypeHeartbeat

		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		if conn == nil {
			protocol.ReleaseMessage(msg)
			errChan <- fmt.Errorf("connection closed")
			return
		}

		if err := protocol.WriteMessage(conn, msg); err != nil {
			protocol.ReleaseMessage(msg)
			errChan <- fmt.Errorf("failed to send heartbeat: %w", err)
			return
		}
		protocol.ReleaseMessage(msg)
	}
}

func (c *Client) handleProxy(errChan chan<- error) {
	for {
		msg, err := protocol.ReadMessage(c.conn)
		if err != nil {
			errChan <- fmt.Errorf("failed to read message: %w", err)
			return
		}

		switch msg.Type {
		case protocol.MsgTypeNewProxy:
			go c.handleProxyRequest(msg, errChan)
		case protocol.MsgTypeHeartbeat:
			continue
		default:
			log.Printf("[ERR] Unknown message type: %d\n", msg.Type)
		}
	}
}

func (c *Client) handleProxyRequest(msg *protocol.Message, errChan chan<- error) {
	if c.config.UseUDP {
		c.handleUDPProxy(errChan)
	} else {
		c.handleTCPProxy(errChan)
	}
}

func (c *Client) handleUDPProxy(errChan chan<- error) {
	localAddr := fmt.Sprintf("%s:%d", c.config.LocalAddr, c.config.LocalPort)
	udpAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		errChan <- fmt.Errorf("failed to resolve local UDP address: %w", err)
		return
	}

	localConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		errChan <- fmt.Errorf("failed to listen on local UDP address: %w", err)
		return
	}
	defer localConn.Close()

	serverAddr := fmt.Sprintf("%s:%d", c.config.ServerAddr, c.config.ServerPort)
	remoteAddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		errChan <- fmt.Errorf("failed to resolve remote UDP address: %w", err)
		return
	}

	remoteConn, err := net.DialUDP("udp", nil, remoteAddr)
	if err != nil {
		errChan <- fmt.Errorf("failed to connect to remote UDP address: %w", err)
		return
	}
	defer remoteConn.Close()

	errChan1 := make(chan error, 1)
	go c.proxyUDP(localConn, remoteConn, errChan1)
	err = <-errChan1
	if err != nil {
		errChan <- err
	}
}

func (c *Client) handleTCPProxy(errChan chan<- error) {
	localAddr := fmt.Sprintf("%s:%d", c.config.LocalAddr, c.config.LocalPort)
	local, err := net.Dial("tcp", localAddr)
	if err != nil {
		errChan <- fmt.Errorf("failed to connect to local service: %w", err)
		return
	}
	defer local.Close()

	errChan1 := make(chan error, 1)
	go c.proxyTCP(local, c.conn, errChan1)
	err = <-errChan1
	if err != nil {
		errChan <- err
	}
}

func (c *Client) proxyTCP(src net.Conn, dst net.Conn, errChan chan<- error) {
	buf := c.getBuf(4096)
	defer c.putBuf(buf)

	for {
		n, err := src.Read(buf)
		if err != nil {
			errChan <- err
			return
		}

		data := buf[:n]
		if src == c.conn {
			data, err = c.crypto.Decrypt(data)
			if err != nil {
				errChan <- fmt.Errorf("failed to decrypt data: %w", err)
				return
			}
		} else {
			data, err = c.crypto.Encrypt(data)
			if err != nil {
				errChan <- fmt.Errorf("failed to encrypt data: %w", err)
				return
			}
		}

		if _, err := dst.Write(data); err != nil {
			errChan <- err
			return
		}
	}
}

func (c *Client) proxyUDP(src, dst *net.UDPConn, errChan chan<- error) {
	buf := c.getBuf(65507)
	defer c.putBuf(buf)

	for {
		n, srcAddr, err := src.ReadFromUDP(buf)
		if err != nil {
			errChan <- fmt.Errorf("failed to read UDP data: %w", err)
			return
		}

		data := buf[:n]
		if src == dst { // 如果是从本地到远程，需要加密
			data, err = c.crypto.Encrypt(data)
			if err != nil {
				errChan <- fmt.Errorf("failed to encrypt UDP data: %w", err)
				return
			}
		} else { // 如果是从远程到本地，需要解密
			data, err = c.crypto.Decrypt(data)
			if err != nil {
				errChan <- fmt.Errorf("failed to decrypt UDP data: %w", err)
				return
			}
		}

		_, err = dst.Write(data)
		if err != nil {
			errChan <- fmt.Errorf("failed to write UDP data: %w", err)
			return
		}

		log.Printf("[INFO] Forwarded %d bytes from %s\n", len(data), srcAddr)
	}
}

func (c *Client) getBuf(size int) []byte {
	buf := c.bufferPool.Get().([]byte)
	if cap(buf) < size {
		return make([]byte, size)
	}
	return buf[:size]
}

func (c *Client) putBuf(buf []byte) {
	if cap(buf) <= 4096 {
		c.bufferPool.Put(buf[:0])
	}
}
