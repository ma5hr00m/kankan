package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/pierrec/lz4/v4"
)

const (
	MsgTypeHeartbeat = iota
	MsgTypeAuth
	MsgTypeAuthOK
	MsgTypeData
	MsgTypeNewProxy
)

const (
	ProxyTypeTCP = iota
	ProxyTypeUDP
)

const (
	FlagCompressed = 1 << iota
	FlagEncrypted
	FlagPadding
	FlagWebSocket
	FlagHTTP
)

var commonHeaders = []string{
	"User-Agent",
	"Accept",
	"Accept-Language",
	"Accept-Encoding",
	"Connection",
	"Cache-Control",
}

var commonUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Edge/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2.1 Safari/605.1.15",
	// 找时间再找点，或者把这块改为自定义的
}

type Message struct {
	Type    byte
	Flags   byte
	Length  uint32
	Payload []byte
	Padding []byte
}

var messagePool = sync.Pool{
	New: func() interface{} {
		return &Message{}
	},
}

type ObfuscationConfig struct {
	EnableHTTP    bool
	EnableWSS     bool
	PaddingRange  [2]int
	JitterRange   time.Duration
	FragmentSize  int
	EnableTLSLike bool
	DynamicPort   bool
	PortRange     [2]int
}

func generatePadding(min, max int) []byte {
	if min >= max {
		return nil
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min)))
	size := int(n.Int64()) + min
	padding := make([]byte, size)
	rand.Read(padding)
	return padding
}

func generateHTTPHeaders() http.Header {
	headers := http.Header{}
	uaIndex, _ := rand.Int(rand.Reader, big.NewInt(int64(len(commonUserAgents))))
	headers.Set("User-Agent", commonUserAgents[uaIndex.Int64()])

	headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	headers.Set("Accept-Language", "en-US,en;q=0.5")
	headers.Set("Accept-Encoding", "gzip, deflate, br")
	headers.Set("Connection", "keep-alive")

	return headers
}

func addJitter(baseDelay time.Duration, maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return baseDelay
	}
	jitter, _ := rand.Int(rand.Reader, big.NewInt(int64(maxJitter)))
	return baseDelay + time.Duration(jitter.Int64())
}

func AcquireMessage() *Message {
	return messagePool.Get().(*Message)
}

func ReleaseMessage(msg *Message) {
	msg.Payload = msg.Payload[:0]
	msg.Padding = msg.Padding[:0]
	messagePool.Put(msg)
}

func ReadMessage(r io.Reader) (*Message, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	msg := AcquireMessage()
	msg.Type = header[0]
	msg.Flags = header[1]
	msg.Length = binary.BigEndian.Uint32(header[2:])

	if msg.Length > 0 {
		msg.Payload = make([]byte, msg.Length)
		if _, err := io.ReadFull(r, msg.Payload); err != nil {
			ReleaseMessage(msg)
			return nil, err
		}

		if msg.Flags&FlagCompressed != 0 {
			decompressed, err := decompress(msg.Payload)
			if err != nil {
				ReleaseMessage(msg)
				return nil, err
			}
			msg.Payload = decompressed
			msg.Length = uint32(len(decompressed))
		}
	}

	return msg, nil
}

func WriteMessage(w io.Writer, msg *Message) error {
	header := make([]byte, 6)
	header[0] = msg.Type
	header[1] = msg.Flags

	payload := msg.Payload
	if len(payload) > 1024 {
		compressed, err := compress(payload)
		if err == nil && len(compressed) < len(payload) {
			payload = compressed
			msg.Flags |= FlagCompressed
		}
	}

	binary.BigEndian.PutUint32(header[2:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}

	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}

	return nil
}

func WriteMessageWithObfuscation(w io.Writer, msg *Message, config *ObfuscationConfig) error {
	if config != nil {
		if config.PaddingRange[1] > config.PaddingRange[0] {
			msg.Padding = generatePadding(config.PaddingRange[0], config.PaddingRange[1])
			msg.Flags |= FlagPadding
		}

		if config.EnableHTTP {
			msg.Flags |= FlagHTTP
			headers := generateHTTPHeaders()
			var buf bytes.Buffer
			headers.Write(&buf)
			msg.Payload = append(buf.Bytes(), msg.Payload...)
		}
	}

	header := make([]byte, 6)
	header[0] = msg.Type
	header[1] = msg.Flags
	binary.BigEndian.PutUint32(header[2:], msg.Length)

	if _, err := w.Write(header); err != nil {
		return err
	}

	if len(msg.Payload) > 0 {
		if _, err := w.Write(msg.Payload); err != nil {
			return err
		}
	}

	if msg.Flags&FlagPadding != 0 && len(msg.Padding) > 0 {
		if _, err := w.Write(msg.Padding); err != nil {
			return err
		}
	}

	return nil
}

func ReadMessageWithObfuscation(r io.Reader, config *ObfuscationConfig) (*Message, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	msg := AcquireMessage()
	msg.Type = header[0]
	msg.Flags = header[1]
	msg.Length = binary.BigEndian.Uint32(header[2:])

	if msg.Length > 0 {
		msg.Payload = make([]byte, msg.Length)
		if _, err := io.ReadFull(r, msg.Payload); err != nil {
			ReleaseMessage(msg)
			return nil, err
		}

		if msg.Flags&FlagCompressed != 0 {
			decompressed, err := decompress(msg.Payload)
			if err != nil {
				ReleaseMessage(msg)
				return nil, err
			}
			msg.Payload = decompressed
			msg.Length = uint32(len(decompressed))
		}
	}

	if msg.Flags&FlagPadding != 0 && config != nil && config.PaddingRange[1] > 0 {
		paddingSize := config.PaddingRange[1]
		msg.Padding = make([]byte, paddingSize)
		if _, err := io.ReadFull(r, msg.Padding); err != nil {
			ReleaseMessage(msg)
			return nil, err
		}
	}

	return msg, nil
}

func FragmentMessage(msg *Message, size int) []*Message {
	if size <= 0 || len(msg.Payload) <= size {
		return []*Message{msg}
	}

	totalSize := len(msg.Payload)
	numFragments := (totalSize + size - 1) / size
	fragments := make([]*Message, numFragments)

	for i := 0; i < numFragments; i++ {
		start := i * size
		end := start + size
		if end > totalSize {
			end = totalSize
		}

		fragment := AcquireMessage()
		fragment.Type = msg.Type
		fragment.Flags = msg.Flags
		fragment.Payload = make([]byte, end-start)
		copy(fragment.Payload, msg.Payload[start:end])
		fragment.Length = uint32(len(fragment.Payload))

		fragments[i] = fragment
	}

	return fragments
}

func compress(data []byte) ([]byte, error) {
	buf := make([]byte, lz4.CompressBlockBound(len(data)))
	n, err := lz4.CompressBlock(data, buf, nil)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func decompress(data []byte) ([]byte, error) {
	buf := make([]byte, len(data)*3)
	n, err := lz4.UncompressBlock(data, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
