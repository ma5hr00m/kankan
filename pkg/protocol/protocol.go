package protocol

import (
	"encoding/binary"
	"io"
)

const (
	MsgTypeHeartbeat = iota
	MsgTypeAuth
	MsgTypeData
	MsgTypeNewProxy
)

type Message struct {
	Type    byte
	Length  uint32
	Payload []byte
}

func ReadMessage(r io.Reader) (*Message, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	msg := &Message{
		Type:   header[0],
		Length: binary.BigEndian.Uint32(header[1:]),
	}

	if msg.Length > 0 {
		msg.Payload = make([]byte, msg.Length)
		if _, err := io.ReadFull(r, msg.Payload); err != nil {
			return nil, err
		}
	}

	return msg, nil
}

func WriteMessage(w io.Writer, msg *Message) error {
	header := make([]byte, 5)
	header[0] = msg.Type
	binary.BigEndian.PutUint32(header[1:], msg.Length)

	if _, err := w.Write(header); err != nil {
		return err
	}

	if msg.Length > 0 {
		if _, err := w.Write(msg.Payload); err != nil {
			return err
		}
	}

	return nil
}
