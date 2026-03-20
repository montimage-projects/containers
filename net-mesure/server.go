package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}

type ServerConfig struct {
	Port    int
	Key     []byte
	Verbose bool
	JSON    bool
}

func runServer(cfg ServerConfig) {
	addr := fmt.Sprintf(":%d", cfg.Port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatalf("listen UDP %s: %v", addr, err)
	}
	defer conn.Close()

	if cfg.JSON {
		type startupMsg struct {
			Status   string `json:"status"`
			Version  string `json:"version"`
			Mode     string `json:"mode"`
			Port     int    `json:"port"`
			HMACAuth bool   `json:"hmac_auth"`
		}
		enc := jsonEncoder(os.Stdout)
		enc.Encode(startupMsg{
			Status:   "listening",
			Version:  version,
			Mode:     "server",
			Port:     cfg.Port,
			HMACAuth: true,
		})
	} else {
		fmt.Printf("╔══════════════════════════════════════════════╗\n")
		fmt.Printf("║        netmeasure v%-5s — SERVER MODE       ║\n", version)
		fmt.Printf("╚══════════════════════════════════════════════╝\n")
		fmt.Printf("  Listening   : UDP %s\n", addr)
		fmt.Printf("  Auth        : header HMAC-SHA256 (truncated 32-bit)\n\n")
	}

	// Per-client probe state: monotonically increasing ServerSeq counter.
	type probeState struct {
		mu  sync.Mutex
		seq uint32
	}

	// Per-client BW session state.
	// recvCount  — total TypeBWUp packets received
	// maxSeq     — highest seq seen (for uplink reorder detection by client)
	type bwSession struct {
		mu        sync.Mutex
		active    bool
		recvCount uint64
		maxSeq    uint64
		firstRecv time.Time
		lastRecv  time.Time
	}

	var probeClients sync.Map // addr -> *probeState
	var bwSessions   sync.Map // addr -> *bwSession

	buf := make([]byte, 65536)

	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("ReadFrom: %v", err)
			continue
		}

		raw := make([]byte, n)
		copy(raw, buf[:n])
		fromStr := from.String()

		pktType, _, err := open(raw, cfg.Key)
		if err != nil {
			if cfg.Verbose {
				log.Printf("[%s] dropped: %v", fromStr, err)
			}
			continue
		}

		switch pktType {

		// ── Probe ─────────────────────────────────────────────────────────
		case TypeProbe:
			_, probe, err := openProbe(raw, cfg.Key)
			if err != nil {
				if cfg.Verbose {
					log.Printf("[%s] bad probe: %v", fromStr, err)
				}
				continue
			}
			val, _ := probeClients.LoadOrStore(fromStr, &probeState{})
			ps := val.(*probeState)
			ps.mu.Lock()
			ps.seq++
			srvSeq := ps.seq
			ps.mu.Unlock()

			if cfg.Verbose {
				log.Printf("[%s] probe cSeq=%d sSeq=%d", fromStr, probe.ClientSeq, srvSeq)
			}

			reply, err := sealProbe(TypeProbeReply, ProbePacket{
				ClientSeq: probe.ClientSeq,
				SendNs:    probe.SendNs,
				ServerSeq: srvSeq,
			}, cfg.Key)
			if err != nil {
				log.Printf("seal reply: %v", err)
				continue
			}
			if _, err := conn.WriteTo(reply, from); err != nil && cfg.Verbose {
				log.Printf("[%s] write reply: %v", fromStr, err)
			}

		// ── Uplink BW data ─────────────────────────────────────────────────
		case TypeBWUp:
			_, seq, err := openBW(raw, cfg.Key)
			if err != nil {
				if cfg.Verbose {
					log.Printf("[%s] bad BWUp: %v", fromStr, err)
				}
				continue
			}
			val, _ := bwSessions.LoadOrStore(fromStr, &bwSession{})
			sess := val.(*bwSession)
			sess.mu.Lock()
			if !sess.active {
				sess.active = true
				sess.firstRecv = time.Now()
				sess.recvCount = 0
				sess.maxSeq = 0
			}
			sess.recvCount++
			if seq > sess.maxSeq {
				sess.maxSeq = seq
			}
			sess.lastRecv = time.Now()
			sess.mu.Unlock()

			if cfg.Verbose && seq%200 == 0 {
				log.Printf("[%s] BWUp seq=%d", fromStr, seq)
			}

		// ── Uplink done: report stats then flood downlink ──────────────────
		case TypeBWUpDone:
			doneMsg, err := openBWUpDone(raw, cfg.Key)
			if err != nil {
				if cfg.Verbose {
					log.Printf("[%s] bad BWUpDone: %v", fromStr, err)
				}
				continue
			}

			val, _ := bwSessions.LoadOrStore(fromStr, &bwSession{})
			sess := val.(*bwSession)
			sess.mu.Lock()
			uplinkRecv := sess.recvCount
			maxULSeq   := sess.maxSeq
			var uplinkDurNs uint64
			if !sess.firstRecv.IsZero() && sess.recvCount > 1 {
				uplinkDurNs = uint64(sess.lastRecv.Sub(sess.firstRecv).Nanoseconds())
			}
			if uplinkDurNs == 0 {
				uplinkDurNs = 1
			}
			sess.active = false
			sess.recvCount = 0
			sess.maxSeq = 0
			sess.mu.Unlock()

			if cfg.Verbose {
				log.Printf("[%s] BWUpDone: clientSent=%d srvRecv=%d maxSeq=%d dur=%v",
					fromStr, doneMsg.TotalSent, uplinkRecv, maxULSeq,
					time.Duration(uplinkDurNs))
			}

			capturedFrom := from
			go func() {
				// ── Send uplink stats report ──────────────────────────────
				statsMsg, err := sealBWDownDone(BWDownDonePacket{
					UplinkRecv: uplinkRecv,
					DurationNs: uplinkDurNs, // != 0 signals "this is the stats report"
					DLSent:     0,
					MaxULSeq:   maxULSeq,
				}, cfg.Key)
				if err != nil {
					log.Printf("seal BWDownDone stats: %v", err)
					return
				}
				for i := 0; i < 5; i++ {
					conn.WriteTo(statsMsg, capturedFrom)
					time.Sleep(5 * time.Millisecond)
				}

				// ── Flood downlink ────────────────────────────────────────
				pktPayload := int(doneMsg.PktSize)
				if pktPayload < bwSeqSize+1 {
					pktPayload = 1400 - headerSize
				}
				padding := pktPayload - bwSeqSize

				dlDeadline := time.Now().Add(time.Duration(doneMsg.DurationNs))
				var seq uint64
				for time.Now().Before(dlDeadline) {
					pkt, err := sealBW(TypeBWDown, seq, padding, cfg.Key)
					if err != nil {
						break
					}
					conn.WriteTo(pkt, capturedFrom)
					seq++
				}

				if cfg.Verbose {
					log.Printf("[%s] downlink done: sent %d pkts", capturedFrom.String(), seq)
				}

				// ── Send sentinel (DurationNs == 0) ───────────────────────
				sentinel, _ := sealBWDownDone(BWDownDonePacket{
					UplinkRecv: uplinkRecv,
					DurationNs: 0,    // sentinel flag
					DLSent:     seq,  // total downlink packets sent
					MaxULSeq:   maxULSeq,
				}, cfg.Key)
				for i := 0; i < 10; i++ {
					conn.WriteTo(sentinel, capturedFrom)
					time.Sleep(10 * time.Millisecond)
				}
			}()

		// ── Hello handshake ───────────────────────────────────────────────
		case TypeHello:
			_, hello, err := openHello(raw, cfg.Key)
			if err != nil {
				if cfg.Verbose {
					log.Printf("[%s] bad hello: %v", fromStr, err)
				}
				continue
			}

			if cfg.Verbose {
				clientLocalIP := hello.LocalIP.String()
				if ip4 := hello.LocalIP.To4(); ip4 != nil {
					clientLocalIP = ip4.String()
				}
				log.Printf("[%s] HELLO  remote=%s  local=%s:%d  version=%q",
					fromStr, fromStr, clientLocalIP, hello.LocalPort, hello.Version)
			}

			ack, err := sealHello(TypeHelloAck, HelloPacket{
				LocalIP:   hello.LocalIP,
				LocalPort: hello.LocalPort,
				Version:   version,
			}, cfg.Key)
			if err != nil {
				log.Printf("seal HelloAck: %v", err)
				continue
			}
			if _, err := conn.WriteTo(ack, from); err != nil && cfg.Verbose {
				log.Printf("[%s] write HelloAck: %v", fromStr, err)
			}

		default:
			if cfg.Verbose {
				log.Printf("[%s] unknown type 0x%02x", fromStr, pktType)
			}
		}
	}
}