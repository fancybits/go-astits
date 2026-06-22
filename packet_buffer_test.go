package astits

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/asticode/go-astikit"
	"github.com/stretchr/testify/assert"
)

func TestAutoDetectPacketSize(t *testing.T) {
	// Packet should start with a sync byte
	buf := &bytes.Buffer{}
	w := astikit.NewBitsWriter(astikit.BitsWriterOptions{Writer: buf})
	w.Write(uint8(2))
	w.Write(byte(syncByte))
	_, err := autoDetectPacketSize(bytes.NewReader(buf.Bytes()))
	assert.EqualError(t, err, ErrPacketMustStartWithASyncByte.Error())

	// Valid packet size
	buf.Reset()
	w.Write(byte(syncByte))
	w.Write(make([]byte, 20))
	w.Write(byte(syncByte))
	w.Write(make([]byte, 166))
	w.Write(byte(syncByte))
	w.Write(make([]byte, 187))
	w.Write([]byte("test"))
	r := bytes.NewReader(buf.Bytes())
	p, err := autoDetectPacketSize(r)
	assert.NoError(t, err)
	assert.Equal(t, MpegTsPacketSize, p)
	assert.Equal(t, 380, r.Len())
}

func TestPacketWithInitialGarbage(t *testing.T) {
	buf := &bytes.Buffer{}
	w := astikit.NewBitsWriter(astikit.BitsWriterOptions{Writer: buf})
	w.Write(uint8(2))
	w.Write(byte(syncByte))
	_, err := autoDetectPacketSize(bytes.NewReader(buf.Bytes()))
	assert.EqualError(t, err, ErrPacketMustStartWithASyncByte.Error())

	// Invalid data and then sync byte
	w.Write(make([]byte, 20))
	w.Write(byte(syncByte))
	w.Write(make([]byte, 20))
	w.Write(byte(syncByte))
	w.Write(make([]byte, 166))
	w.Write(byte(syncByte))
	w.Write(make([]byte, 187))
	w.Write([]byte("test"))
	r := bytes.NewReader(buf.Bytes())

	pb, err := newPacketBuffer(r, 188, nil, false)
	assert.NoError(t, err)
	p, err := pb.next()
	assert.NoError(t, err)
	assert.NotNil(t, p)
}

func TestPacketBufferNextPeek(t *testing.T) {
	// Build a few TS packets.
	buf := &bytes.Buffer{}
	w := astikit.NewBitsWriter(astikit.BitsWriterOptions{Writer: buf})
	for cc := 0; cc < 3; cc++ {
		b, _ := packet(PacketHeader{ContinuityCounter: uint8(cc), HasPayload: true, PID: 256}, PacketAdaptationField{}, []byte("payload"), false)
		w.Write(b)
	}
	stream := buf.Bytes()

	read := func(r io.Reader) []*Packet {
		pb, err := newPacketBuffer(r, MpegTsPacketSize, nil, false)
		assert.NoError(t, err)
		var ps []*Packet
		for {
			p, perr := pb.next()
			if perr == ErrNoMorePackets {
				break
			}
			assert.NoError(t, perr)
			ps = append(ps, p)
		}
		return ps
	}

	// A raw reader uses the ReadFull path; a bufio.Reader uses the Peek path.
	// Both must yield identical packets.
	fromRaw := read(bytes.NewReader(stream))
	fromBufio := read(bufio.NewReader(bytes.NewReader(stream)))
	assert.Len(t, fromRaw, 3)
	assert.Equal(t, fromRaw, fromBufio)
}
