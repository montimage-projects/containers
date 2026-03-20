package main

// client.go — measurement logic, statistics, and output
//
// Every metric is reported with four summary values: Min, Max, Avg, Median.
//
// Sample populations:
//   RTT           — one sample per received probe reply (ms)
//   Jitter        — |RTT[i] - RTT[i-1]| for consecutive seq-ordered pairs (ms)
//   Probe loss    — per-second loss % (uplink and downlink separately)
//   Probe reorder — per-second reorder % (RFC 4737, uplink and downlink)
//   BW loss       — per-second loss % measured during flood (UL server-side, DL client-side)
//   BW reorder    — per-second reorder % measured during flood
//   Bandwidth     — per-second Mbps during flood (UL client-side, DL client-side)
//
// If the BW phase is skipped, probe-phase loss/reorder values are used in the
// final summary for those metrics.

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type ClientConfig struct {
	Host       string
	Port       int
	Iface      string
	Key        []byte
	Count      int
	IntervalMs int
	BWDuration int
	BWPktSize  int
	SkipProbe  bool
	SkipBW     bool
	Verbose    bool
	JSON       bool
}

// ---------------------------------------------------------------------------
// Stats primitive
// ---------------------------------------------------------------------------

type Stats struct {
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Avg    float64 `json:"avg"`
	Median float64 `json:"median"`
}

func (s Stats) MarshalJSON() ([]byte, error) {
	f := func(v float64) json.RawMessage {
		return json.RawMessage(strconv.FormatFloat(v, 'f', 3, 64))
	}
	return json.Marshal(&struct {
		Min    json.RawMessage `json:"min"`
		Max    json.RawMessage `json:"max"`
		Avg    json.RawMessage `json:"avg"`
		Median json.RawMessage `json:"median"`
	}{Min: f(s.Min), Max: f(s.Max), Avg: f(s.Avg), Median: f(s.Median)})
}

func statsOf(samples []float64) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	cp := make([]float64, len(samples))
	copy(cp, samples)
	sort.Float64s(cp)
	n := len(cp)
	sum := 0.0
	for _, v := range cp {
		sum += v
	}
	var median float64
	if n%2 == 0 {
		median = (cp[n/2-1] + cp[n/2]) / 2
	} else {
		median = cp[n/2]
	}
	return Stats{Min: cp[0], Max: cp[n-1], Avg: sum / float64(n), Median: median}
}

// ---------------------------------------------------------------------------
// Report structures
// ---------------------------------------------------------------------------

type DirStats struct {
	LossPct    Stats `json:"loss_pct"`
	ReorderPct Stats `json:"reorder_pct"`
}

// ProbeReport covers RTT, jitter, and probe-phase loss/reorder.
type ProbeReport struct {
	ProbeSent       int      `json:"probes_sent"`
	ProbeServerRecv int      `json:"probes_server_received"`
	ProbeClientRecv int      `json:"probes_client_received"`
	RTT             Stats    `json:"rtt_ms"`
	Jitter          Stats    `json:"jitter_ms"`
	Uplink          DirStats `json:"uplink"`
	Downlink        DirStats `json:"downlink"`
}

// BWReport covers bandwidth and BW-phase loss/reorder.
// If BWReport is nil (phase skipped), the consumer falls back to ProbeReport.
type BWReport struct {
	PacketSizeBytes int      `json:"packet_size_bytes"`
	Uplink          BWDirReport `json:"uplink"`
	Downlink        BWDirReport `json:"downlink"`
}

type BWDirReport struct {
	BandwidthMbps Stats `json:"bandwidth_mbps"`
	LossPct       Stats `json:"loss_pct"`
	ReorderPct    Stats `json:"reorder_pct"`
}

// JSONReport is the top-level output when --json is set.
type JSONReport struct {
	Version   string       `json:"version"`
	Timestamp string       `json:"timestamp"`
	Target    string       `json:"target"`
	Interface string       `json:"interface,omitempty"`
	Probe     *ProbeReport `json:"probe,omitempty"`
	Bandwidth *BWReport    `json:"bandwidth,omitempty"`
	// Summary shows the "best available" loss/reorder: BW phase if run, else probe phase.
	Summary   SummaryReport `json:"summary"`
}

type SummaryReport struct {
	RTT             Stats    `json:"rtt_ms"`
	Jitter          Stats    `json:"jitter_ms"`
	UplinkLoss      Stats    `json:"uplink_loss_pct"`
	DownlinkLoss    Stats    `json:"downlink_loss_pct"`
	UplinkReorder   Stats    `json:"uplink_reorder_pct"`
	DownlinkReorder Stats    `json:"downlink_reorder_pct"`
	UplinkBW        Stats    `json:"uplink_bw_mbps"`
	DownlinkBW      Stats    `json:"downlink_bw_mbps"`
	LossSource      string   `json:"loss_reorder_source"` // "bw_phase" or "probe_phase"
	BWSource        string   `json:"bw_source,omitempty"`
}

// ---------------------------------------------------------------------------
// NIC / dial helpers
// ---------------------------------------------------------------------------

func resolveLocalIP(iface string) (string, error) {
	if iface == "" {
		return "", nil
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", fmt.Errorf("interface %q not found: %w", iface, err)
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", fmt.Errorf("addresses for %q: %w", iface, err)
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String(), nil
		}
		return "[" + ip.String() + "]", nil
	}
	return "", fmt.Errorf("no usable IP address on interface %q", iface)
}

func dialUDP(localIP, remoteHost string, remotePort int) (*net.UDPConn, error) {
	remoteAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", remoteHost, remotePort))
	if err != nil {
		return nil, fmt.Errorf("resolve %s:%d: %w", remoteHost, remotePort, err)
	}
	var localAddr *net.UDPAddr
	if localIP != "" {
		localAddr = &net.UDPAddr{IP: net.ParseIP(localIP)}
	}
	conn, err := net.DialUDP("udp", localAddr, remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("dial UDP: %w", err)
	}
	return conn, nil
}

// ---------------------------------------------------------------------------
// Hello handshake
// ---------------------------------------------------------------------------

func sendHello(cfg ClientConfig, localIP string) error {
	conn, err := dialUDP(localIP, cfg.Host, cfg.Port)
	if err != nil {
		return fmt.Errorf("hello dial: %w", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	helloIP := net.ParseIP(localAddr.IP.String())
	if helloIP == nil {
		helloIP = net.IPv4(0, 0, 0, 0)
	}

	helloPkt, err := sealHello(TypeHello, HelloPacket{
		LocalIP:   helloIP,
		LocalPort: uint16(localAddr.Port),
		Version:   version,
	}, cfg.Key)
	if err != nil {
		return fmt.Errorf("seal hello: %w", err)
	}

	const maxAttempts = 5
	const retryInterval = 2 * time.Second
	buf := make([]byte, 65536)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := conn.Write(helloPkt); err != nil {
			return fmt.Errorf("send hello: %w", err)
		}
		if !cfg.JSON {
			fmt.Printf("  [hello] attempt %d/%d → %s:%d (local %s:%d)\n",
				attempt, maxAttempts, cfg.Host, cfg.Port,
				localAddr.IP, localAddr.Port)
		}

		conn.SetReadDeadline(time.Now().Add(retryInterval))
		n, err := conn.Read(buf)
		if err != nil {
			if cfg.Verbose && !cfg.JSON {
				fmt.Printf("  [hello] no reply, retrying…\n")
			}
			continue
		}

		pktType, ack, err := openHello(buf[:n], cfg.Key)
		if err != nil {
			return fmt.Errorf("bad HelloAck: %w", err)
		}
		if pktType != TypeHelloAck {
			return fmt.Errorf("expected HelloAck (0x%02x), got 0x%02x", TypeHelloAck, pktType)
		}
		if !cfg.JSON {
			fmt.Printf("  [hello] ack  server-version=%q\n\n", ack.Version)
		}
		return nil
	}
	return fmt.Errorf("server did not respond to hello after %d attempts", maxAttempts)
}

// ---------------------------------------------------------------------------
// Top-level runner
// ---------------------------------------------------------------------------

func runClient(cfg ClientConfig) {
	localIP, err := resolveLocalIP(cfg.Iface)
	if err != nil {
		fatalf("%v", err)
	}

	report := JSONReport{
		Version:   version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Target:    fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
	if cfg.Iface != "" {
		report.Interface = cfg.Iface
	}

	printBanner(cfg, localIP)

	// ── Hello handshake — must succeed before any measurement ────────────
	if !cfg.JSON {
		fmt.Println("━━━  Handshake  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}
	if err := sendHello(cfg, localIP); err != nil {
		fatalf("handshake failed: %v", err)
	}

	var pr  *ProbeReport
	var bwr *BWReport

	if !cfg.SkipProbe {
		if !cfg.JSON {
			fmt.Println("━━━  Phase 1 — RTT · Jitter · Loss · Reordering  ━━━━━━━━━━━━")
		}
		pr = runProbePhase(cfg, localIP)
		report.Probe = pr
		if !cfg.JSON && pr != nil {
			printProbeReport(pr)
		}
	}

	if !cfg.SkipBW {
		if !cfg.JSON {
			fmt.Println("\n━━━  Phase 2 — Bandwidth  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		}
		bwr = runBWPhase(cfg, localIP)
		report.Bandwidth = bwr
		if !cfg.JSON && bwr != nil {
			printBWReport(bwr)
		}
	}

	// Build summary — prefer BW-phase loss/reorder, fall back to probe phase.
	report.Summary = buildSummary(pr, bwr)

	if cfg.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
	} else {
		fmt.Println("\n━━━  Summary  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		printSummary(report.Summary)
	}
}

func buildSummary(pr *ProbeReport, bwr *BWReport) SummaryReport {
	s := SummaryReport{}

	// RTT and jitter always come from probe phase.
	if pr != nil {
		s.RTT    = pr.RTT
		s.Jitter = pr.Jitter
	}

	// Loss and reorder: use BW phase if available (higher volume = more accurate).
	if bwr != nil {
		s.UplinkLoss      = bwr.Uplink.LossPct
		s.DownlinkLoss    = bwr.Downlink.LossPct
		s.UplinkReorder   = bwr.Uplink.ReorderPct
		s.DownlinkReorder = bwr.Downlink.ReorderPct
		s.LossSource      = "bw_phase"
	} else if pr != nil {
		s.UplinkLoss      = pr.Uplink.LossPct
		s.DownlinkLoss    = pr.Downlink.LossPct
		s.UplinkReorder   = pr.Uplink.ReorderPct
		s.DownlinkReorder = pr.Downlink.ReorderPct
		s.LossSource      = "probe_phase"
	}

	// Bandwidth always from BW phase.
	if bwr != nil {
		s.UplinkBW   = bwr.Uplink.BandwidthMbps
		s.DownlinkBW = bwr.Downlink.BandwidthMbps
		s.BWSource   = "bw_phase"
	}

	return s
}

// ---------------------------------------------------------------------------
// Phase 1 — Probe (RTT / jitter / loss / reorder)
// ---------------------------------------------------------------------------

type probeRecord struct {
	clientSeq uint64
	serverSeq uint32
	rttMs     float64
}

func runProbePhase(cfg ClientConfig, localIP string) *ProbeReport {
	conn, err := dialUDP(localIP, cfg.Host, cfg.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ERROR probe dial: %v\n", err)
		return nil
	}
	defer conn.Close()

	if !cfg.JSON {
		fmt.Printf("  Sending %d probes @ %d ms intervals → %s:%d\n",
			cfg.Count, cfg.IntervalMs, cfg.Host, cfg.Port)
		if localIP != "" {
			fmt.Printf("  Bound to interface %q (%s)\n", cfg.Iface, localIP)
		}
		fmt.Println()
	}

	var mu sync.Mutex
	var records []probeRecord // arrival order

	rxDone := make(chan struct{})
	go func() {
		defer close(rxDone)
		buf := make([]byte, 65536)
		for {
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			recvNs := time.Now().UnixNano()
			pktType, probe, err := openProbe(buf[:n], cfg.Key)
			if err != nil || pktType != TypeProbeReply {
				if cfg.Verbose && err != nil {
					fmt.Printf("  [rx] bad packet: %v\n", err)
				}
				continue
			}
			rttMs := float64(recvNs-probe.SendNs) / 1e6
			mu.Lock()
			records = append(records, probeRecord{
				clientSeq: probe.ClientSeq,
				serverSeq: probe.ServerSeq,
				rttMs:     rttMs,
			})
			mu.Unlock()
			if cfg.Verbose {
				fmt.Printf("  ← cSeq=%-5d sSeq=%-5d RTT=%8.3f ms\n",
					probe.ClientSeq, probe.ServerSeq, rttMs)
			}
		}
	}()

	sendStart := time.Now()
	interval := time.Duration(cfg.IntervalMs) * time.Millisecond
	for i := 0; i < cfg.Count; i++ {
		pkt, err := sealProbe(TypeProbe, ProbePacket{
			ClientSeq: uint64(i + 1),
			SendNs:    time.Now().UnixNano(),
		}, cfg.Key)
		if err != nil {
			continue
		}
		conn.Write(pkt)
		if i < cfg.Count-1 {
			time.Sleep(interval)
		}
	}
	_ = sendStart

	linger := 2*time.Second + time.Duration(cfg.IntervalMs*2)*time.Millisecond
	if linger > 5*time.Second {
		linger = 5 * time.Second
	}
	time.Sleep(linger)
	conn.Close()
	<-rxDone

	mu.Lock()
	snap := make([]probeRecord, len(records))
	copy(snap, records)
	mu.Unlock()

	return computeProbeReport(snap, cfg.Count, cfg.IntervalMs)
}

func computeProbeReport(records []probeRecord, sent, intervalMs int) *ProbeReport {
	recv := len(records)

	// Sort by clientSeq for RTT/jitter.
	bySeq := make([]probeRecord, recv)
	copy(bySeq, records)
	sort.Slice(bySeq, func(i, j int) bool { return bySeq[i].clientSeq < bySeq[j].clientSeq })

	// RTT samples
	rttSamples := make([]float64, recv)
	for i, r := range bySeq {
		rttSamples[i] = r.rttMs
	}

	// Jitter samples
	var jitterSamples []float64
	for i := 1; i < len(bySeq); i++ {
		jitterSamples = append(jitterSamples, math.Abs(bySeq[i].rttMs-bySeq[i-1].rttMs))
	}

	// max server seq seen = total server received
	var maxSrvSeq uint32
	for _, r := range bySeq {
		if r.serverSeq > maxSrvSeq {
			maxSrvSeq = r.serverSeq
		}
	}
	srvGot := int(maxSrvSeq)

	// Per-second buckets for loss and reorder
	bucketSize := int(time.Second / (time.Duration(intervalMs) * time.Millisecond))
	if bucketSize < 1 {
		bucketSize = 1
	}

	var ulLossSamples, dlLossSamples []float64
	var ulReorderSamples, dlReorderSamples []float64

	for start := 0; start < sent; start += bucketSize {
		end := start + bucketSize
		if end > sent {
			end = sent
		}
		bucketSent := end - start

		var bucketRecv []probeRecord
		for _, r := range records {
			seq := int(r.clientSeq)
			if seq > start && seq <= end {
				bucketRecv = append(bucketRecv, r)
			}
		}

		var bucketSrvMin, bucketSrvMax uint32
		if len(bucketRecv) > 0 {
			bucketSrvMin = bucketRecv[0].serverSeq
			bucketSrvMax = bucketRecv[0].serverSeq
			for _, r := range bucketRecv[1:] {
				if r.serverSeq < bucketSrvMin {
					bucketSrvMin = r.serverSeq
				}
				if r.serverSeq > bucketSrvMax {
					bucketSrvMax = r.serverSeq
				}
			}
		}
		srvRecvInBucket := 0
		if len(bucketRecv) > 0 {
			srvRecvInBucket = int(bucketSrvMax-bucketSrvMin) + 1
		}
		ulLostInBucket := bucketSent - srvRecvInBucket
		if ulLostInBucket < 0 {
			ulLostInBucket = 0
		}
		ulLossSamples = append(ulLossSamples,
			float64(ulLostInBucket)/float64(bucketSent)*100)

		dlLostInBucket := srvRecvInBucket - len(bucketRecv)
		if dlLostInBucket < 0 {
			dlLostInBucket = 0
		}
		if srvRecvInBucket > 0 {
			dlLossSamples = append(dlLossSamples,
				float64(dlLostInBucket)/float64(srvRecvInBucket)*100)
		} else {
			dlLossSamples = append(dlLossSamples, 0)
		}

		if len(bucketRecv) >= 2 {
			ulSeqs := make([]uint64, len(bucketRecv))
			dlSeqs := make([]uint64, len(bucketRecv))
			for i, r := range bucketRecv {
				ulSeqs[i] = uint64(r.serverSeq)
				dlSeqs[i] = r.clientSeq
			}
			ulReorderSamples = append(ulReorderSamples, reorderPct(ulSeqs))
			dlReorderSamples = append(dlReorderSamples, reorderPct(dlSeqs))
		}
	}

	_ = srvGot
	return &ProbeReport{
		ProbeSent:       sent,
		ProbeServerRecv: srvGot,
		ProbeClientRecv: recv,
		RTT:             statsOf(rttSamples),
		Jitter:          statsOf(jitterSamples),
		Uplink: DirStats{
			LossPct:    statsOf(ulLossSamples),
			ReorderPct: statsOf(ulReorderSamples),
		},
		Downlink: DirStats{
			LossPct:    statsOf(dlLossSamples),
			ReorderPct: statsOf(dlReorderSamples),
		},
	}
}

func printProbeReport(p *ProbeReport) {
	fmt.Printf("  Probes sent       : %d\n", p.ProbeSent)
	fmt.Printf("  Server received   : %d\n", p.ProbeServerRecv)
	fmt.Printf("  Client received   : %d\n", p.ProbeClientRecv)
	fmt.Println()
	printStatsRow("RTT              (ms)", p.RTT)
	printStatsRow("Jitter           (ms)", p.Jitter)
	fmt.Println()
	fmt.Println("  Direction  Metric           Min      Max      Avg      Median")
	fmt.Println("  ─────────  ───────────────  ───────  ───────  ───────  ───────")
	printDirStatsRow("Uplink  ", "Loss        (%)", p.Uplink.LossPct)
	printDirStatsRow("Uplink  ", "Reorder     (%)", p.Uplink.ReorderPct)
	printDirStatsRow("Downlink", "Loss        (%)", p.Downlink.LossPct)
	printDirStatsRow("Downlink", "Reorder     (%)", p.Downlink.ReorderPct)
}

func printStatsRow(label string, s Stats) {
	fmt.Printf("  %-22s  min=%8.3f  max=%8.3f  avg=%8.3f  median=%8.3f\n",
		label, s.Min, s.Max, s.Avg, s.Median)
}

func printDirStatsRow(dir, label string, s Stats) {
	fmt.Printf("  %s   %-15s %7.2f  %7.2f  %7.2f  %7.2f\n",
		dir, label, s.Min, s.Max, s.Avg, s.Median)
}

func reorderPct(seqs []uint64) float64 {
	if len(seqs) < 2 {
		return 0
	}
	ooo := 0
	maxSeen := seqs[0]
	for _, s := range seqs[1:] {
		if s < maxSeen {
			ooo++
		} else {
			maxSeen = s
		}
	}
	return float64(ooo) / float64(len(seqs)-1) * 100
}

// ---------------------------------------------------------------------------
// Phase 2 — Bandwidth (with per-second loss and reorder tracking)
// ---------------------------------------------------------------------------

// bwSecBucket accumulates per-second BW stats for one direction.
type bwSecBucket struct {
	bytes    int64
	minSeq   uint64
	maxSeq   uint64
	count    uint64
	hasFirst bool
}

func runBWPhase(cfg ClientConfig, localIP string) *BWReport {
	conn, err := dialUDP(localIP, cfg.Host, cfg.Port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ERROR bw dial: %v\n", err)
		return nil
	}
	defer conn.Close()

	pktPayload := cfg.BWPktSize - headerSize
	if pktPayload < bwSeqSize+1 {
		pktPayload = 1400 - headerSize
	}
	paddingSize := pktPayload - bwSeqSize

	if !cfg.JSON {
		fmt.Printf("  UDP size   : %d B (%d B header + %d B payload)\n",
			cfg.BWPktSize, headerSize, pktPayload)
		fmt.Printf("  UL/DL dur  : %d s each\n", cfg.BWDuration)
		if localIP != "" {
			fmt.Printf("  Interface  : %q (%s)\n", cfg.Iface, localIP)
		}
		fmt.Println()
	}

	// ── Uplink flood with per-second sampling ─────────────────────────────
	if !cfg.JSON {
		fmt.Printf("  [uplink]  flooding for %d s …\n", cfg.BWDuration)
	}

	var ulMbpsSamples   []float64
	var ulLossSamples   []float64
	var ulReorderSamples []float64

	ulDeadline := time.Now().Add(time.Duration(cfg.BWDuration) * time.Second)
	var ulSent uint64
	secTick := time.NewTicker(time.Second)
	ulBucket := bwSecBucket{}

	flushULBucket := func() {
		if ulBucket.count == 0 {
			return
		}
		mbps := float64(ulBucket.bytes) * 8 / 1e6
		ulMbpsSamples = append(ulMbpsSamples, mbps)
		// Loss within this second: expected = maxSeq-minSeq+1, got = count
		expected := ulBucket.maxSeq - ulBucket.minSeq + 1
		lost := int64(expected) - int64(ulBucket.count)
		if lost < 0 {
			lost = 0
		}
		ulLossSamples = append(ulLossSamples,
			float64(lost)/float64(expected)*100)
		ulBucket = bwSecBucket{}
	}

	for time.Now().Before(ulDeadline) {
		select {
		case <-secTick.C:
			flushULBucket()
		default:
		}
		pkt, err := sealBW(TypeBWUp, ulSent, paddingSize, cfg.Key)
		if err != nil {
			break
		}
		n, _ := conn.Write(pkt)
		if !ulBucket.hasFirst {
			ulBucket.minSeq = ulSent
			ulBucket.hasFirst = true
		}
		ulBucket.maxSeq = ulSent
		ulBucket.count++
		ulBucket.bytes += int64(n)
		ulSent++
	}
	secTick.Stop()
	flushULBucket()

	// Uplink reorder: client-side we only know send order is sequential;
	// actual UL reorder is measured server-side via maxULSeq in BWDownDone.
	// We populate ulReorderSamples after receiving the server report.

	if !cfg.JSON {
		fmt.Printf("  [uplink]  sent %d pkts\n\n", ulSent)
	}

	// ── Signal uplink done ────────────────────────────────────────────────
	dlDurNs := uint64(time.Duration(cfg.BWDuration) * time.Second)
	doneMsg, err := sealBWUpDone(BWUpDonePacket{
		TotalSent:  ulSent,
		DurationNs: dlDurNs,
		PktSize:    uint64(pktPayload),
	}, cfg.Key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ERROR seal BWUpDone: %v\n", err)
		return nil
	}
	for i := 0; i < 10; i++ {
		conn.Write(doneMsg)
		time.Sleep(5 * time.Millisecond)
	}

	// ── Downlink receive with per-second sampling ─────────────────────────
	if !cfg.JSON {
		fmt.Printf("  [downlink] receiving for %d s …\n", cfg.BWDuration)
	}

	var dlMbpsSamples    []float64
	var dlLossSamples    []float64
	var dlReorderSamples []float64

	var serverULRecv  uint64
	var serverMaxULSeq uint64
	var serverULDurNs uint64
	gotULStats := false

	var dlMu sync.Mutex
	dlBucket := bwSecBucket{}

	flushDLBucket := func() {
		dlMu.Lock()
		defer dlMu.Unlock()
		if dlBucket.count == 0 {
			return
		}
		mbps := float64(dlBucket.bytes) * 8 / 1e6
		dlMbpsSamples = append(dlMbpsSamples, mbps)
		expected := dlBucket.maxSeq - dlBucket.minSeq + 1
		lost := int64(expected) - int64(dlBucket.count)
		if lost < 0 {
			lost = 0
		}
		dlLossSamples = append(dlLossSamples,
			float64(lost)/float64(expected)*100)
		dlBucket = bwSecBucket{}
	}

	dlSecTicker := time.NewTicker(time.Second)
	dlTickQuit := make(chan struct{})
	dlTickDone := make(chan struct{})
	go func() {
		defer close(dlTickDone)
		for {
			select {
			case <-dlSecTicker.C:
				flushDLBucket()
			case <-dlTickQuit:
				return
			}
		}
	}()

	// We also track per-second arrival-order seqs for DL reorder.
	// Collect all received seqs then bucket them by arrival time.
	type dlRecord struct {
		seq     uint64
		arrTime time.Time
	}
	var dlRecords []dlRecord
	dlStart := time.Now()

	buf := make([]byte, 65536)
	conn.SetReadDeadline(time.Now().Add(time.Duration(cfg.BWDuration)*time.Second + 10*time.Second))

dlLoop:
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		pktType, _, err := open(buf[:n], cfg.Key)
		if err != nil {
			if cfg.Verbose {
				fmt.Printf("  [dl rx] bad packet: %v\n", err)
			}
			continue
		}
		switch pktType {
		case TypeBWDown:
			_, seq, err := openBW(buf[:n], cfg.Key)
			if err != nil {
				continue
			}
			arrTime := time.Now()
			dlMu.Lock()
			if !dlBucket.hasFirst {
				dlBucket.minSeq = seq
				dlBucket.hasFirst = true
			}
			if seq > dlBucket.maxSeq {
				dlBucket.maxSeq = seq
			}
			dlBucket.count++
			dlBucket.bytes += int64(n)
			dlMu.Unlock()
			dlRecords = append(dlRecords, dlRecord{seq: seq, arrTime: arrTime})
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		case TypeBWDownDone:
			done, err := openBWDownDone(buf[:n], cfg.Key)
			if err != nil {
				continue
			}
			if done.DurationNs != 0 && !gotULStats {
				serverULRecv   = done.UplinkRecv
				serverMaxULSeq = done.MaxULSeq
				serverULDurNs  = done.DurationNs
				gotULStats = true
				if cfg.Verbose {
					fmt.Printf("  [bw] server UL: recv=%d maxSeq=%d dur=%v\n",
						serverULRecv, serverMaxULSeq,
						time.Duration(serverULDurNs))
				}
			} else if done.DurationNs == 0 {
				break dlLoop
			}
		}
	}

	dlSecTicker.Stop()
	close(dlTickQuit)
	<-dlTickDone
	flushDLBucket() // flush final partial second

	dlDur := time.Since(dlStart)
	if !cfg.JSON {
		fmt.Printf("  [downlink] received %d pkts in %v\n\n",
			len(dlRecords), dlDur.Round(time.Millisecond))
	}

	// ── Compute downlink reorder per-second ───────────────────────────────
	// Bucket dlRecords by second of arrival, compute reorderPct per bucket.
	if len(dlRecords) > 0 {
		bucketStart := dlRecords[0].arrTime.Truncate(time.Second)
		var seqBuf []uint64
		for _, r := range dlRecords {
			if r.arrTime.Before(bucketStart.Add(time.Second)) {
				seqBuf = append(seqBuf, r.seq)
			} else {
				if len(seqBuf) >= 2 {
					dlReorderSamples = append(dlReorderSamples, reorderPct(seqBuf))
				}
				bucketStart = r.arrTime.Truncate(time.Second)
				seqBuf = []uint64{r.seq}
			}
		}
		if len(seqBuf) >= 2 {
			dlReorderSamples = append(dlReorderSamples, reorderPct(seqBuf))
		}
	}

	// ── Compute uplink reorder from server-reported maxULSeq ─────────────
	// Server sent us: total received (serverULRecv) and max seq (serverMaxULSeq).
	// If packets arrived in order, maxSeq == recvCount-1 (0-based).
	// Gap = maxSeq - (recvCount-1) gives a proxy for out-of-order arrivals.
	// We synthesise a single summary sample; if the full test ran more than
	// one second we replicate it as the per-second estimate.
	if gotULStats && serverULRecv > 0 {
		var ulOOO float64
		if serverMaxULSeq+1 > serverULRecv {
			ulOOO = float64(serverMaxULSeq+1-serverULRecv) / float64(serverMaxULSeq+1) * 100
		}
		// One sample per second of the uplink duration
		nSec := int(time.Duration(serverULDurNs).Seconds())
		if nSec < 1 {
			nSec = 1
		}
		for i := 0; i < nSec; i++ {
			ulReorderSamples = append(ulReorderSamples, ulOOO)
		}
	}

	// ── Uplink loss from server counts ────────────────────────────────────
	if gotULStats && ulSent > 0 {
		// Replace client-side UL loss samples with server-side truth.
		// Server received serverULRecv out of ulSent.
		ulLossSamples = nil
		nSec := int(time.Duration(serverULDurNs).Seconds())
		if nSec < 1 {
			nSec = 1
		}
		overallLoss := float64(int64(ulSent)-int64(serverULRecv)) / float64(ulSent) * 100
		if overallLoss < 0 {
			overallLoss = 0
		}
		for i := 0; i < nSec; i++ {
			ulLossSamples = append(ulLossSamples, overallLoss)
		}
	}

	_ = serverULDurNs

	return &BWReport{
		PacketSizeBytes: cfg.BWPktSize,
		Uplink: BWDirReport{
			BandwidthMbps: statsOf(ulMbpsSamples),
			LossPct:       statsOf(ulLossSamples),
			ReorderPct:    statsOf(ulReorderSamples),
		},
		Downlink: BWDirReport{
			BandwidthMbps: statsOf(dlMbpsSamples),
			LossPct:       statsOf(dlLossSamples),
			ReorderPct:    statsOf(dlReorderSamples),
		},
	}
}

func printBWReport(r *BWReport) {
	fmt.Printf("  Packet size : %d bytes (%d B auth header + payload)\n\n",
		r.PacketSizeBytes, headerSize)
	fmt.Println("  Direction   Metric           Min      Max      Avg      Median")
	fmt.Println("  ──────────  ───────────────  ───────  ───────  ───────  ───────")
	printDirStatsRow("Uplink  ", "BW      (Mbps)", r.Uplink.BandwidthMbps)
	printDirStatsRow("Uplink  ", "Loss      (%)", r.Uplink.LossPct)
	printDirStatsRow("Uplink  ", "Reorder   (%)", r.Uplink.ReorderPct)
	printDirStatsRow("Downlink", "BW      (Mbps)", r.Downlink.BandwidthMbps)
	printDirStatsRow("Downlink", "Loss      (%)", r.Downlink.LossPct)
	printDirStatsRow("Downlink", "Reorder   (%)", r.Downlink.ReorderPct)
}

func printSummary(s SummaryReport) {
	fmt.Printf("  Loss/reorder source : %s\n\n", s.LossSource)
	printStatsRow("RTT              (ms)", s.RTT)
	printStatsRow("Jitter           (ms)", s.Jitter)
	fmt.Println()
	fmt.Println("  Direction   Metric           Min      Max      Avg      Median")
	fmt.Println("  ──────────  ───────────────  ───────  ───────  ───────  ───────")
	printDirStatsRow("Uplink  ", "BW      (Mbps)", s.UplinkBW)
	printDirStatsRow("Uplink  ", "Loss      (%)", s.UplinkLoss)
	printDirStatsRow("Uplink  ", "Reorder   (%)", s.UplinkReorder)
	printDirStatsRow("Downlink", "BW      (Mbps)", s.DownlinkBW)
	printDirStatsRow("Downlink", "Loss      (%)", s.DownlinkLoss)
	printDirStatsRow("Downlink", "Reorder   (%)", s.DownlinkReorder)
}