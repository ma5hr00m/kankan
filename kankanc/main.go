package main

import (
	"flag"
	"fmt"
	"io"
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
}

var config Config

func init() {
	flag.StringVar(&config.ServerAddr, "server", "127.0.0.1", "Server address")
	flag.IntVar(&config.ServerPort, "sport", 8080, "Server port")
	flag.StringVar(&config.LocalAddr, "local", "127.0.0.1", "Local service address")
	flag.IntVar(&config.LocalPort, "lport", 80, "Local service port")
	flag.StringVar(&config.Key, "key", "", "Encryption key")
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
	config Config
	conn   net.Conn
	crypto *crypto.Crypto
	mu     sync.Mutex
}

func NewClient(config Config) *Client {
	return &Client{
		config: config,
	}
}

func (c *Client) Start() error {
	for {
		if err := c.connect(); err != nil {
			log.Printf("[ERR] Connection failed: %v, retrying in 5s\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		c.handleConnection()
	}
}

func (c *Client) connect() error {
	serverAddr := fmt.Sprintf("%s:%d", c.config.ServerAddr, c.config.ServerPort)
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("[ERR] Failed to connect to server: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	crypto, err := crypto.NewCrypto(c.config.Key)
	if err != nil {
		return fmt.Errorf("[ERR] Failed to create crypto instance: %w", err)
	}
	c.crypto = crypto

	if err := c.authenticate(); err != nil {
		return fmt.Errorf("[ERR] Authentication failed: %w", err)
	}

	log.Printf("[INFO] Connected to server %s\n", serverAddr)
	return nil
}

func (c *Client) authenticate() error {
	msg := &protocol.Message{
		Type:    protocol.MsgTypeAuth,
		Length:  0,
		Payload: nil,
	}
	return protocol.WriteMessage(c.conn, msg)
}

func (c *Client) handleConnection() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	go c.heartbeat()

	for {
		msg, err := protocol.ReadMessage(c.conn)
		if err != nil {
			log.Printf("[ERR] Failed to read message: %v\n", err)
			return
		}

		switch msg.Type {
		case protocol.MsgTypeNewProxy:
			go c.handleProxy()
		case protocol.MsgTypeHeartbeat:
			continue
		default:
			log.Printf("[ERR] Unknown message type: %d\n", msg.Type)
		}
	}
}

func (c *Client) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			msg := &protocol.Message{
				Type:    protocol.MsgTypeHeartbeat,
				Length:  0,
				Payload: nil,
			}
			c.mu.Lock()
			if c.conn == nil {
				c.mu.Unlock()
				return
			}
			err := protocol.WriteMessage(c.conn, msg)
			c.mu.Unlock()
			if err != nil {
				log.Printf("[ERR] Failed to send heartbeat: %v\n", err)
				return
			}
		}
	}
}

func (c *Client) handleProxy() {
	localAddr := fmt.Sprintf("%s:%d", c.config.LocalAddr, c.config.LocalPort)
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("[ERR] Failed to connect to local service: %v\n", err)
		return
	}
	defer localConn.Close()

	c.mu.Lock()
	serverConn := c.conn
	c.mu.Unlock()

	if serverConn == nil {
		return
	}

	go func() {
		for {
			msg, err := protocol.ReadMessage(serverConn)
			if err != nil {
				return
			}

			if msg.Type != protocol.MsgTypeData {
				continue
			}

			data, err := c.crypto.Decrypt(msg.Payload)
			if err != nil {
				log.Printf("[ERR] Failed to decrypt data: %v\n", err)
				return
			}

			if _, err := localConn.Write(data); err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := localConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("[ERR] Failed to read from local service: %v\n", err)
			}
			return
		}

		data, err := c.crypto.Encrypt(buf[:n])
		if err != nil {
			log.Printf("[ERR] Failed to encrypt data: %v\n", err)
			return
		}

		msg := &protocol.Message{
			Type:    protocol.MsgTypeData,
			Length:  uint32(len(data)),
			Payload: data,
		}

		c.mu.Lock()
		err = protocol.WriteMessage(serverConn, msg)
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}
