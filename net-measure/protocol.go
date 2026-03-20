package main

// ---------------------------------------------------------------------------
// Wire protocol — UDP-only
// ---------------------------------------------------------------------------
//
// Authentication model (changed from full-payload HMAC):
//   Every datagram has a fixed 13-byte authenticated header:
//     [0]    Type    uint8
//     [1-2]  Length  uint16   total datagram length in bytes (header + payload)
//     [3-10] Nonce   uint64   random per-packet value (replay mitigation)
//     [11-14] HMAC32 uint32   first 4 bytes of HMAC-SHA256(key, Type||Length||Nonce)
//
//   Only Type, Length, and Nonce are authenticated — the payload is sent in
//   the clear.  This is intentional: bulk BW data does not need confidentiality
//   and the truncated MAC is sufficient to authenticate the control header and
//   reject unauthenticated/spoofed packets with negligible false-positive rate
//   (1 in 2^32 per packet).  Control packets (Hello, Probe, BWUpDone, etc.)
//   are small enough that the 13-byte overhead is negligible.
//
// Header layout (13 bytes):
//   [0]    Type    uint8
//   [1-2]  Length  uint16  big-endian, total datagram size
//   [3-10] Nonce   uint64  big-endian random
//   [11-14] MAC    uint32  big-endian, truncated HMAC-SHA256 over header fields
//
// Packet types and payloads (all offsets relative to start of payload, i.e. buf[headerSize:]):
//
//   TypeProbe      (0x01)  Client->Server  20 B payload
//     [0-7]   ClientSeq  uint64
//     [8-15]  SendNs     int64   unix nanosecond timestamp
//     [16-19] ServerSeq  uint32  (zero in request; filled by server in reply)
//
//   TypeProbeReply (0x02)  Server->Client  20 B payload
//     same layout, ServerSeq filled
//
//   TypeBWUp       (0x03)  Client->Server  8+N B payload
//     [0-7]  Seq      uint64
//     [8-N]  padding
//
//   TypeBWUpDone   (0x04)  Client->Server  32 B payload
//     [0-7]   TotalSent    uint64  TypeBWUp packets client sent
//     [8-15]  DurationNs   uint64  requested downlink duration (ns)
//     [16-23] PktSize      uint64  requested downlink packet payload size (bytes)
//     [24-31] MaxSrvSeqUp  uint64  highest server-seq seen in uplink (for UL reorder; unused here, set 0)
//
//   TypeBWDown     (0x05)  Server->Client  8+N B payload
//     [0-7]  Seq      uint64
//     [8-N]  padding
//
//   TypeBWDownDone (0x06)  Server->Client  32 B payload
//     [0-7]   UplinkRecv   uint64  packets server received on uplink
//     [8-15]  DurationNs   uint64  uplink receive window ns; 0 = sentinel "downlink done"
//     [16-23] DLSent       uint64  total downlink packets server sent (in sentinel)
//     [24-31] MaxULSeq     uint64  max uplink seq server received (for UL reorder detection)
//
//   TypeHello      (0x07)  Client->Server  variable payload
//     [0-15]  LocalIP    16 B  IPv4-in-IPv6
//     [16-17] LocalPort  uint16
//     [18]    VerLen     uint8
//     [19-N]  Version    []byte
//
//   TypeHelloAck   (0x08)  Server->Client  same layout as TypeHello

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

const (
	TypeProbe      uint8 = 0x01
	TypeProbeReply uint8 = 0x02
	TypeBWUp       uint8 = 0x03
	TypeBWUpDone   uint8 = 0x04
	TypeBWDown     uint8 = 0x05
	TypeBWDownDone uint8 = 0x06
	TypeHello      uint8 = 0x07
	TypeHelloAck   uint8 = 0x08
)

const (
	headerSize   = 13 // Type(1) + Length(2) + Nonce(8) + MAC32(4) — was 41
	macTruncSize = 4  // bytes of HMAC-SHA256 used as the packet MAC
	envelopeSize = headerSize
)

var (
	ErrBadMAC = errors.New("HMAC verification failed")
	ErrShort  = errors.New("packet too short")
)

// ---------------------------------------------------------------------------
// Header auth — seal / open
// ---------------------------------------------------------------------------

// seal builds a datagram: 13-byte authenticated header + plaintext payload.
func seal(pktType uint8, payload, key []byte) ([]byte, error) {
	total := headerSize + len(payload)
	if total > 0xFFFF {
		return nil, fmt.Errorf("payload too large: %d", len(payload))
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	mac := headerMAC(pktType, uint16(total), nonce, key)

	out := make([]byte, total)
	out[0] = pktType
	binary.BigEndian.PutUint16(out[1:3], uint16(total))
	copy(out[3:11], nonce)
	copy(out[11:15], mac[:macTruncSize])
	copy(out[headerSize:], payload)
	return out, nil
}

// open verifies the header MAC and returns (type, payload, error).
// Payload is a slice into buf — copy if you need to keep it.
func open(buf, key []byte) (uint8, []byte, error) {
	if len(buf) < headerSize {
		return 0, nil, ErrShort
	}
	pktType := buf[0]
	length  := binary.BigEndian.Uint16(buf[1:3])
	nonce   := buf[3:11]
	gotMAC  := buf[11:15]
	if int(length) > len(buf) {
		return 0, nil, fmt.Errorf("length field %d > buf %d", length, len(buf))
	}
	wantMAC := headerMAC(pktType, length, nonce, key)
	if !hmac.Equal(gotMAC, wantMAC[:macTruncSize]) {
		return 0, nil, ErrBadMAC
	}
	return pktType, buf[headerSize:length], nil
}

func headerMAC(pktType uint8, length uint16, nonce, key []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte{pktType})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	h.Write(lb[:])
	h.Write(nonce)
	return h.Sum(nil)
}

// ---------------------------------------------------------------------------
// ProbePacket  (TypeProbe / TypeProbeReply)
// ---------------------------------------------------------------------------

const probePayloadSize = 20

type ProbePacket struct {
	ClientSeq uint64
	SendNs    int64
	ServerSeq uint32
}

func encodeProbePayload(p ProbePacket) []byte {
	b := make([]byte, probePayloadSize)
	binary.BigEndian.PutUint64(b[0:8], p.ClientSeq)
	binary.BigEndian.PutUint64(b[8:16], uint64(p.SendNs))
	binary.BigEndian.PutUint32(b[16:20], p.ServerSeq)
	return b
}

func decodeProbePayload(b []byte) (ProbePacket, error) {
	if len(b) < probePayloadSize {
		return ProbePacket{}, fmt.Errorf("probe payload short: %d", len(b))
	}
	return ProbePacket{
		ClientSeq: binary.BigEndian.Uint64(b[0:8]),
		SendNs:    int64(binary.BigEndian.Uint64(b[8:16])),
		ServerSeq: binary.BigEndian.Uint32(b[16:20]),
	}, nil
}

func sealProbe(pktType uint8, p ProbePacket, key []byte) ([]byte, error) {
	return seal(pktType, encodeProbePayload(p), key)
}

func openProbe(buf, key []byte) (uint8, ProbePacket, error) {
	t, payload, err := open(buf, key)
	if err != nil {
		return 0, ProbePacket{}, err
	}
	p, err := decodeProbePayload(payload)
	return t, p, err
}

// ---------------------------------------------------------------------------
// BW data packets (TypeBWUp / TypeBWDown) — variable size
// ---------------------------------------------------------------------------

const bwSeqSize = 8

// sealBW builds a BW packet: header + seq(8) + padding.
func sealBW(pktType uint8, seq uint64, paddingSize int, key []byte) ([]byte, error) {
	payload := make([]byte, bwSeqSize+paddingSize)
	binary.BigEndian.PutUint64(payload[0:8], seq)
	return seal(pktType, payload, key)
}

// openBW returns (type, seq, error).
func openBW(buf, key []byte) (uint8, uint64, error) {
	t, payload, err := open(buf, key)
	if err != nil {
		return 0, 0, err
	}
	if len(payload) < bwSeqSize {
		return 0, 0, fmt.Errorf("bw payload short: %d", len(payload))
	}
	return t, binary.BigEndian.Uint64(payload[0:8]), nil
}

// ---------------------------------------------------------------------------
// BWUpDone  (TypeBWUpDone)  — client signals end of uplink
// ---------------------------------------------------------------------------

const bwUpDonePayloadSize = 24

type BWUpDonePacket struct {
	TotalSent  uint64 // TypeBWUp packets client sent
	DurationNs uint64 // requested downlink duration (ns)
	PktSize    uint64 // requested downlink packet payload size (bytes, excl. header)
}

func sealBWUpDone(p BWUpDonePacket, key []byte) ([]byte, error) {
	b := make([]byte, bwUpDonePayloadSize)
	binary.BigEndian.PutUint64(b[0:8], p.TotalSent)
	binary.BigEndian.PutUint64(b[8:16], p.DurationNs)
	binary.BigEndian.PutUint64(b[16:24], p.PktSize)
	return seal(TypeBWUpDone, b, key)
}

func openBWUpDone(buf, key []byte) (BWUpDonePacket, error) {
	_, payload, err := open(buf, key)
	if err != nil {
		return BWUpDonePacket{}, err
	}
	if len(payload) < bwUpDonePayloadSize {
		return BWUpDonePacket{}, fmt.Errorf("BWUpDone payload short: %d", len(payload))
	}
	return BWUpDonePacket{
		TotalSent:  binary.BigEndian.Uint64(payload[0:8]),
		DurationNs: binary.BigEndian.Uint64(payload[8:16]),
		PktSize:    binary.BigEndian.Uint64(payload[16:24]),
	}, nil
}

// ---------------------------------------------------------------------------
// BWDownDone  (TypeBWDownDone)  — server reports uplink stats + signals DL end
// ---------------------------------------------------------------------------
//
// Two uses distinguished by DurationNs:
//   DurationNs != 0  →  uplink stats report (sent early, downlink still running)
//   DurationNs == 0  →  sentinel: downlink finished
//
// Extra fields vs old version:
//   DLSent    — total downlink packets server sent (filled in sentinel)
//   MaxULSeq  — highest TypeBWUp seq server received (for UL reorder detection)

const bwDownDonePayloadSize = 32

type BWDownDonePacket struct {
	UplinkRecv uint64 // packets server received on uplink
	DurationNs uint64 // uplink window ns; 0 = sentinel
	DLSent     uint64 // total DL pkts sent (meaningful in sentinel)
	MaxULSeq   uint64 // max uplink seq received by server (for reorder)
}

func sealBWDownDone(p BWDownDonePacket, key []byte) ([]byte, error) {
	b := make([]byte, bwDownDonePayloadSize)
	binary.BigEndian.PutUint64(b[0:8], p.UplinkRecv)
	binary.BigEndian.PutUint64(b[8:16], p.DurationNs)
	binary.BigEndian.PutUint64(b[16:24], p.DLSent)
	binary.BigEndian.PutUint64(b[24:32], p.MaxULSeq)
	return seal(TypeBWDownDone, b, key)
}

func openBWDownDone(buf, key []byte) (BWDownDonePacket, error) {
	_, payload, err := open(buf, key)
	if err != nil {
		return BWDownDonePacket{}, err
	}
	if len(payload) < bwDownDonePayloadSize {
		return BWDownDonePacket{}, fmt.Errorf("BWDownDone payload short: %d", len(payload))
	}
	return BWDownDonePacket{
		UplinkRecv: binary.BigEndian.Uint64(payload[0:8]),
		DurationNs: binary.BigEndian.Uint64(payload[8:16]),
		DLSent:     binary.BigEndian.Uint64(payload[16:24]),
		MaxULSeq:   binary.BigEndian.Uint64(payload[24:32]),
	}, nil
}

// ---------------------------------------------------------------------------
// HelloPacket  (TypeHello / TypeHelloAck)
// ---------------------------------------------------------------------------

const helloFixedSize = 19 // IP(16) + Port(2) + VerLen(1)

type HelloPacket struct {
	LocalIP   net.IP
	LocalPort uint16
	Version   string
}

func sealHello(pktType uint8, h HelloPacket, key []byte) ([]byte, error) {
	verBytes := []byte(h.Version)
	if len(verBytes) > 32 {
		verBytes = verBytes[:32]
	}
	payload := make([]byte, helloFixedSize+len(verBytes))
	ip16 := h.LocalIP.To16()
	if ip16 == nil {
		ip16 = make([]byte, 16)
	}
	copy(payload[0:16], ip16)
	binary.BigEndian.PutUint16(payload[16:18], h.LocalPort)
	payload[18] = uint8(len(verBytes))
	copy(payload[19:], verBytes)
	return seal(pktType, payload, key)
}

func openHello(buf, key []byte) (uint8, HelloPacket, error) {
	pktType, payload, err := open(buf, key)
	if err != nil {
		return 0, HelloPacket{}, err
	}
	if len(payload) < helloFixedSize {
		return 0, HelloPacket{}, fmt.Errorf("hello payload short: %d", len(payload))
	}
	ip := net.IP(make([]byte, 16))
	copy(ip, payload[0:16])
	port := binary.BigEndian.Uint16(payload[16:18])
	verLen := int(payload[18])
	var ver string
	if verLen > 0 && len(payload) >= helloFixedSize+verLen {
		ver = string(payload[19 : 19+verLen])
	}
	return pktType, HelloPacket{
		LocalIP:   ip,
		LocalPort: port,
		Version:   ver,
	}, nil
}