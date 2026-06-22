package astits

import (
	"encoding/binary"
	"fmt"
	"github.com/asticode/go-astikit"
)

// PIDs
const (
	PIDPAT  uint16 = 0x0    // Program Association Table (PAT) contains a directory listing of all Program Map Tables.
	PIDCAT  uint16 = 0x1    // Conditional Access Table (CAT) contains a directory listing of all ITU-T Rec. H.222 entitlement management message streams used by Program Map Tables.
	PIDTSDT uint16 = 0x2    // Transport Stream Description Table (TSDT) contains descriptors related to the overall transport stream
	PIDNull uint16 = 0x1fff // Null Packet (used for fixed bandwidth padding)
)

// DemuxerData represents a data parsed by Demuxer
type DemuxerData struct {
	EIT         *EITData
	FirstPacket *Packet
	NIT         *NITData
	PAT         *PATData
	PES         *PESData
	PID         uint16
	PMT         *PMTData
	SDT         *SDTData
	TOT         *TOTData
}

// MuxerData represents a data to be written by Muxer
type MuxerData struct {
	PID             uint16
	AdaptationField *PacketAdaptationField
	PES             *PESData
}

// parseData parses a payload spanning over multiple packets and returns a set of data
func parseData(ps []*Packet, prs PacketsParser, pm *programMap, noCopyPayload bool) (ds []*DemuxerData, poolItem *bytesPoolItem, err error) {
	// Use custom parser first
	if prs != nil {
		var skip bool
		if ds, skip, err = prs(ps); err != nil {
			err = fmt.Errorf("astits: custom packets parsing failed: %w", err)
			return
		} else if skip {
			return
		}
	}

	// Get payload length
	var l int
	for _, p := range ps {
		l += len(p.Payload)
	}

	// Get the slice for payload from pool. Normally it is returned to the pool
	// when we are done here, but in no-copy mode the PES payload below points
	// into it, so ownership is transferred to the caller via poolItem instead.
	payload := bytesPool.get(l)
	returnPayload := true
	defer func() {
		if returnPayload {
			bytesPool.put(payload)
		}
	}()

	// Append payload
	var c int
	for _, p := range ps {
		c += copy(payload.s[c:], p.Payload)
	}

	// Create reader
	i := astikit.NewBytesIterator(payload.s)

	// Parse PID
	pid := ps[0].Header.PID

	// Copy first packet headers, so we can safely deallocate original payload
	fp := &Packet{
		Header:          ps[0].Header,
		AdaptationField: ps[0].AdaptationField,
	}

	// Parse payload
	if pid == PIDCAT {
		// Information in a CAT payload is private and dependent on the CA system. Use the PacketsParser
		// to parse this type of payload
	} else if isPSIPayload(pid, pm) {
		// Parse PSI data
		var psiData *PSIData
		if psiData, err = parsePSIData(i); err != nil {
			err = fmt.Errorf("astits: parsing PSI data failed: %w", err)
			return
		}

		// Append data
		ds = psiData.toData(fp, pid)
	} else if isPESPayload(payload.s) {
		// Parse PES data
		var pesData *PESData
		if pesData, err = parsePESData(i, noCopyPayload); err != nil {
			err = fmt.Errorf("astits: parsing PES data failed: %w", err)
			return
		}

		// In no-copy mode pesData.Data points into payload.s, so keep the pooled
		// buffer alive and hand it to the caller to release once consumed.
		if noCopyPayload {
			returnPayload = false
			poolItem = payload
		}

		// Append data
		ds = []*DemuxerData{
			{
				FirstPacket: fp,
				PES:         pesData,
				PID:         pid,
			},
		}
	}
	return
}

// isPSIPayload checks whether the payload is a PSI one
func isPSIPayload(pid uint16, pm *programMap) bool {
	return pid == PIDPAT || // PAT
		pm.existsUnlocked(pid) || // PMT
		((pid >= 0x10 && pid <= 0x14) || (pid >= 0x1e && pid <= 0x1f)) //DVB
}

// isPESPayload checks whether the payload is a PES one
func isPESPayload(i []byte) bool {
	// Packet is not big enough
	if len(i) < 3 {
		return false
	}

	// Check prefix
	return uint32(i[0])<<16|uint32(i[1])<<8|uint32(i[2]) == 1
}

// isPSIComplete checks whether we have sufficient amount of packets to parse PSI
func isPSIComplete(ps []*Packet) bool {
	// Get payload length
	var l int
	for _, p := range ps {
		l += len(p.Payload)
	}

	// Get the slice for payload from pool
	payload := bytesPool.get(l)
	defer bytesPool.put(payload)

	// Append payload
	var o int
	for _, p := range ps {
		o += copy(payload.s[o:], p.Payload)
	}

	// Create reader
	i := astikit.NewBytesIterator(payload.s)

	// Get next byte
	b, err := i.NextByte()
	if err != nil {
		return false
	}

	// Pointer filler bytes
	i.Skip(int(b))

	for i.HasBytesLeft() {

		// Get PSI table ID
		b, err = i.NextByte()
		if err != nil {
			return false
		}

		// Check whether we need to stop the parsing
		if shouldStopPSIParsing(PSITableID(b)) {
			break
		}

		// Get PSI section length
		var bs []byte
		bs, err = i.NextBytesNoCopy(2)
		if err != nil {
			return false
		}

		i.Skip(int(binary.BigEndian.Uint16(bs) & 0x0fff))
	}

	return i.Len() >= i.Offset()
}

// isPESComplete checks whether payload fully contains PES packet
func isPESComplete(ps []*Packet) bool {
	if len(ps) == 0 {
		return false
	}
	// PES_packet_length is at bytes 4-5 (after the 3-byte start-code prefix and
	// 1-byte stream id), always within the first packet. Read just those two bytes
	// via the iterator the parser is built on, instead of reassembling all packets
	// and parsing the full header below: a zero length marks an unbounded
	// elementary stream (typically video) whose completeness can't be determined
	// here, so bail — otherwise that work runs for every packet of every such PES.
	lenIter := astikit.NewBytesIterator(ps[0].Payload)
	lenIter.Seek(4)
	if pktLen, err := lenIter.NextBytesNoCopy(2); err != nil || (pktLen[0] == 0 && pktLen[1] == 0) {
		return false
	}

	// Get payload length
	var l int
	for _, p := range ps {
		l += len(p.Payload)
	}

	// Get the slice for payload from pool
	payload := bytesPool.get(l)
	defer bytesPool.put(payload)

	// Append payload
	var o int
	for _, p := range ps {
		o += copy(payload.s[o:], p.Payload)
	}

	// Create reader
	i := astikit.NewBytesIterator(payload.s)

	// Skip first 3 bytes that are there to identify the PES payload
	i.Seek(3)

	// Parse header
	h, _, dataEnd, err := parsePESHeader(i)
	if err != nil {
		err = fmt.Errorf("astits: parsing PES header failed: %w", err)
		return false
	}

	if h.PacketLength == 0 {
		// There's no other way to know whether the packet is complete
		return false
	}

	return i.Len() >= dataEnd
}
