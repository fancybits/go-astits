package astits

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pesRecord struct {
	pid uint16
	pes *PESData
	af  *PacketAdaptationField
}

func TestRoundTrip(t *testing.T) {
	originalBytes, err := os.ReadFile("testdata/ts/silent_audio.ts")
	require.NoError(t, err)

	// Phase 1: Demux the original TS file
	dmx := NewDemuxer(context.Background(), bytes.NewReader(originalBytes), DemuxerOptPacketSize(MpegTsPacketSize))

	var originalPAT *PATData
	var originalPMT *PMTData
	var originalPMTPID uint16 = 0xFFFF

	for {
		d, err := dmx.NextData()
		if errors.Is(err, ErrNoMorePackets) {
			break
		}
		require.NoError(t, err)

		if d.PAT != nil {
			originalPAT = d.PAT
			originalPMTPID = d.PAT.Programs[0].ProgramMapID
		}
		if d.PMT != nil {
			originalPMT = d.PMT
		}

		if originalPMT != nil && originalPAT != nil {
			break
		}
	}
	require.NotNil(t, originalPAT)
	require.NotNil(t, originalPMT)
	require.NotEqual(t, 0xFFFF, originalPMTPID)

	// Phase 2: Mux everything back into a new TS stream, preserving PAT/PMT identifiers
	var buf bytes.Buffer
	muxer := NewMuxer(context.Background(), &buf,
		WithTransportStreamID(originalPAT.TransportStreamID),
		WithPMTPID(originalPMTPID),
	)

	for _, es := range originalPMT.ElementaryStreams {
		err := muxer.AddElementaryStream(PMTElementaryStream{
			ElementaryPID:               es.ElementaryPID,
			StreamType:                  es.StreamType,
			ElementaryStreamDescriptors: es.ElementaryStreamDescriptors,
		})
		require.NoError(t, err)
	}
	muxer.SetPCRPID(originalPMT.PCRPID)
	muxer.pmt.ProgramDescriptors = originalPMT.ProgramDescriptors
	_, err = muxer.WriteTables()
	require.NoError(t, err)

	// Phase 3: Demux the round-tripped output
	dmx2 := NewDemuxer(context.Background(), bytes.NewReader(buf.Bytes()), DemuxerOptPacketSize(MpegTsPacketSize))

	var rtPAT *PATData
	var rtPMT *PMTData

	for {
		d, err := dmx2.NextData()
		if errors.Is(err, ErrNoMorePackets) {
			break
		}
		require.NoError(t, err)

		if d.PAT != nil {
			rtPAT = d.PAT
		}
		if d.PMT != nil {
			rtPMT = d.PMT
		}

		if rtPAT != nil && rtPMT != nil {
			break
		}
	}
	require.NotNil(t, rtPAT)
	require.NotNil(t, rtPMT)

	// Phase 4: Validate round-trip preserved all meaningful information
	// --- PAT ---
	assert.Equal(t, originalPAT.TransportStreamID, rtPAT.TransportStreamID, "PAT TransportStreamID mismatch")
	require.Equal(t, len(originalPAT.Programs), len(rtPAT.Programs), "PAT program count mismatch")
	for i, origProg := range originalPAT.Programs {
		assert.Equalf(t, origProg.ProgramNumber, rtPAT.Programs[i].ProgramNumber,
			"PAT Programs[%d].ProgramNumber mismatch", i)
		assert.Equalf(t, origProg.ProgramMapID, rtPAT.Programs[i].ProgramMapID,
			"PAT Programs[%d].ProgramMapID mismatch", i)
	}

	// --- PMT ---
	assert.Equal(t, originalPMT.PCRPID, rtPMT.PCRPID)
	assert.Equal(t, originalPMT.ProgramNumber, rtPMT.ProgramNumber)
	require.Equal(t, len(originalPMT.ProgramDescriptors), len(rtPMT.ProgramDescriptors))
	for i, desc := range originalPMT.ProgramDescriptors {
		assert.Equalf(t, desc.Tag, rtPMT.ProgramDescriptors[i].Tag,
			"PMT ProgramDescriptors[%d].Tag mismatch", i)
		assert.Equalf(t, desc.Length, rtPMT.ProgramDescriptors[i].Length,
			"PMT ProgramDescriptors[%d].Length mismatch", i)
	}
	require.Equal(t, len(originalPMT.ElementaryStreams), len(rtPMT.ElementaryStreams))
	for i, es := range originalPMT.ElementaryStreams {
		rtES := rtPMT.ElementaryStreams[i]
		assert.Equalf(t, es.ElementaryPID, rtES.ElementaryPID,
			"PMT ElementaryStreams[%d].ElementaryPID mismatch", i)
		assert.Equalf(t, es.StreamType, rtES.StreamType,
			"PMT ElementaryStreams[%d].StreamType mismatch", i)
		require.Equalf(t, len(es.ElementaryStreamDescriptors), len(rtES.ElementaryStreamDescriptors),
			"PMT ElementaryStreams[%d].ElementaryStreamDescriptors count mismatch", i)
		for j, desc := range es.ElementaryStreamDescriptors {
			assert.Equalf(t, desc.Tag, rtES.ElementaryStreamDescriptors[j].Tag,
				"PMT ElementaryStreams[%d].Descriptors[%d].Tag mismatch", i, j)
			assert.Equalf(t, desc.Length, rtES.ElementaryStreamDescriptors[j].Length,
				"PMT ElementaryStreams[%d].Descriptors[%d].Length mismatch", i, j)
		}
	}
}
