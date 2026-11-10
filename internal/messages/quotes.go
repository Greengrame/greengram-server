package messages

import (
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func ReplyHeader(id int, raw []byte) *tg.MessageReplyHeader {
	var header tg.MessageReplyHeader
	if len(raw) != 0 {
		header.Decode(&bin.Buffer{Buf: raw})
	}
	header.ReplyToMsgID = id
	return &header
}

func EncodeReplyQuote(header *tg.MessageReplyHeader) ([]byte, error) {
	var buffer bin.Buffer
	var err error
	err = header.Encode(&buffer)
	return buffer.Buf, err
}
