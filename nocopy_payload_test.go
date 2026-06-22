package astits

import (
	"bytes"
	"context"
	"testing"
	"unsafe"

	"github.com/asticode/go-astikit"
	"github.com/stretchr/testify/assert"
)

// noCopyPESStream builds a TS stream of n back-to-back PES on PID 256, each
// spanning two TS packets (split at 33 bytes, mirroring the parseData tests).
func noCopyPESStream(n int) []byte {
	buf := &bytes.Buffer{}
	w := astikit.NewBitsWriter(astikit.BitsWriterOptions{Writer: buf})
	p := pesWithHeaderBytes()
	var cc uint8
	for i := 0; i < n; i++ {
		b1, _ := packet(PacketHeader{ContinuityCounter: cc, PayloadUnitStartIndicator: true, PID: 256, HasPayload: true}, PacketAdaptationField{}, p[:33], false)
		w.Write(b1)
		cc++
		b2, _ := packet(PacketHeader{ContinuityCounter: cc, PID: 256, HasPayload: true}, PacketAdaptationField{}, p[33:], false)
		w.Write(b2)
		cc++
	}
	return buf.Bytes()
}

// TestParseDataNoCopyPayload verifies that no-copy mode produces the same PES
// data as copy mode, transfers ownership of the pooled buffer to the caller,
// and that the returned data aliases that buffer instead of being a fresh copy.
func TestParseDataNoCopyPayload(t *testing.T) {
	pm := newProgramMap()
	p := pesWithHeaderBytes()
	mkPackets := func() []*Packet {
		return []*Packet{
			{Header: PacketHeader{PID: uint16(256)}, Payload: p[:33]},
			{Header: PacketHeader{PID: uint16(256)}, Payload: p[33:]},
		}
	}

	// Copy mode: no pooled buffer is handed back; Data is a fresh copy.
	dsCopy, itemCopy, err := parseData(mkPackets(), nil, pm, false)
	assert.NoError(t, err)
	assert.Nil(t, itemCopy)
	assert.Equal(t, pesWithHeader(), dsCopy[0].PES)

	// No-copy mode: identical PES data, but the pooled buffer is transferred to
	// the caller and Data points into it.
	dsNoCopy, itemNoCopy, err := parseData(mkPackets(), nil, pm, true)
	assert.NoError(t, err)
	assert.NotNil(t, itemNoCopy)
	assert.Equal(t, pesWithHeader(), dsNoCopy[0].PES)

	// Data must alias the pooled buffer rather than being separately allocated.
	data := dsNoCopy[0].PES.Data
	assert.NotEmpty(t, data)
	dataPtr := uintptr(unsafe.Pointer(&data[0]))
	bufStart := uintptr(unsafe.Pointer(&itemNoCopy.s[0]))
	bufEnd := bufStart + uintptr(cap(itemNoCopy.s))
	assert.True(t, dataPtr >= bufStart && dataPtr < bufEnd,
		"no-copy PES data should alias the pooled buffer")

	bytesPool.put(itemNoCopy)
}

// TestDemuxerNoCopyPayload verifies that a single PES (flushed at EOF) demuxed
// with DemuxerOptNoCopyPayload produces the same PES as the default copy path.
func TestDemuxerNoCopyPayload(t *testing.T) {
	read := func(opts ...func(*Demuxer)) *PESData {
		dmx := NewDemuxer(context.Background(), bytes.NewReader(noCopyPESStream(1)),
			append([]func(*Demuxer){DemuxerOptPacketSize(188)}, opts...)...)
		d, err := dmx.NextData()
		assert.NoError(t, err)
		return d.PES
	}

	assert.Equal(t, read(), read(DemuxerOptNoCopyPayload()))
}

// TestDemuxerNoCopyPayloadMultiple drives several PES through NextData, which
// exercises the per-call release of the previous pending buffer. Each payload is
// copied on read (per the contract) and must match the copy path.
func TestDemuxerNoCopyPayloadMultiple(t *testing.T) {
	readAll := func(opts ...func(*Demuxer)) [][]byte {
		dmx := NewDemuxer(context.Background(), bytes.NewReader(noCopyPESStream(3)),
			append([]func(*Demuxer){DemuxerOptPacketSize(188)}, opts...)...)
		var out [][]byte
		for {
			d, err := dmx.NextData()
			if err == ErrNoMorePackets {
				break
			}
			assert.NoError(t, err)
			if d.PES != nil {
				// Only valid until the next NextData in no-copy mode, so copy now.
				out = append(out, append([]byte(nil), d.PES.Data...))
			}
		}
		return out
	}

	got := readAll(DemuxerOptNoCopyPayload())
	assert.GreaterOrEqual(t, len(got), 2, "expected multiple PES")
	assert.Equal(t, readAll(), got)
}

// TestDemuxerNoCopyPayloadRewind verifies Rewind releases the pending pooled
// buffer (so the pool accounting stays balanced) and leaves the demuxer able to
// re-read identical data.
func TestDemuxerNoCopyPayloadRewind(t *testing.T) {
	dmx := NewDemuxer(context.Background(), bytes.NewReader(noCopyPESStream(1)),
		DemuxerOptPacketSize(188), DemuxerOptNoCopyPayload())

	d1, err := dmx.NextData()
	assert.NoError(t, err)
	assert.NotNil(t, dmx.pendingPoolItem)
	first := append([]byte(nil), d1.PES.Data...)

	_, err = dmx.Rewind()
	assert.NoError(t, err)
	assert.Nil(t, dmx.pendingPoolItem, "Rewind must release the pending no-copy buffer")

	d2, err := dmx.NextData()
	assert.NoError(t, err)
	assert.Equal(t, first, d2.PES.Data)
}

// TestDemuxerNoCopyPayloadPacketsParserDisablesPooling verifies that a custom
// PacketsParser disables per-packet payload pooling. Such a parser receives the
// packets and may retain their payloads, so they must not be drawn from the pool
// (and thus must not be recycled).
func TestDemuxerNoCopyPayloadPacketsParserDisablesPooling(t *testing.T) {
	stream := noCopyPESStream(1)

	// Option alone: pooling is enabled.
	dmx := NewDemuxer(context.Background(), bytes.NewReader(stream),
		DemuxerOptPacketSize(188), DemuxerOptNoCopyPayload())
	_, err := dmx.NextPacket()
	assert.NoError(t, err)
	assert.NotNil(t, dmx.packetBuffer.payloadPool, "pooling should be enabled with the option and no custom parser")

	// Option + custom PacketsParser: pooling is disabled.
	pp := func(ps []*Packet) ([]*DemuxerData, bool, error) { return nil, false, nil }
	dmx2 := NewDemuxer(context.Background(), bytes.NewReader(stream),
		DemuxerOptPacketSize(188), DemuxerOptNoCopyPayload(), DemuxerOptPacketsParser(pp))
	p, err := dmx2.NextPacket()
	assert.NoError(t, err)
	assert.Nil(t, dmx2.packetBuffer.payloadPool, "pooling must be disabled when a custom PacketsParser is set")
	assert.Nil(t, p.payloadBuf, "packet payload must not be pooled when a custom parser is set")
}
