A tool to measure network performance: 
- latency (RTT), 
- jitter (variation of RTT), 
- packet loss, 
- packet out-of-order, 
- bandwidth (not provided by [irtt](../irtt))

A HMAC-based authentication mechanism between clients-server to avoid unauthenticated access.

# Build

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o net-measure
```

# Execution
## Server

```bash
$ ./net-measure server
╔══════════════════════════════════════════════╗
║        netmeasure v1.0.0 — SERVER MODE       ║
╚══════════════════════════════════════════════╝
  Listening   : UDP :9000
  HMAC auth   : enabled (SHA-256)

```

## Client

```bash
$ ./net-measure client --iface enp0s3 --host <your-server-ip-above>
╔══════════════════════════════════════════════╗
║     netmeasure v1.0.0 — CLIENT MODE          ║
╚══════════════════════════════════════════════╝
  Target     : cartimia.montimage.com:9000
  Interface  : enp0s3 (10.0.2.15)
  HMAC auth  : enabled (SHA-256)
  Time       : 2026-03-19 16:45:08

━━━  Phase 1 — RTT · Jitter · Loss · Reordering  ━━━━━━━━━━━━
  Sending 10 probes @ 10 ms intervals → cartimia.montimage.com:9000
  Bound to interface "enp0s3" (10.0.2.15)

  Probes sent       : 10
  Server received   : 0
  Client received   : 0

  RTT              (ms)   min=   0.000  max=   0.000  avg=   0.000  median=   0.000
  Jitter           (ms)   min=   0.000  max=   0.000  avg=   0.000  median=   0.000

  Direction  Metric         Min      Max      Avg      Median
  ─────────  ─────────────  ───────  ───────  ───────  ───────
  Uplink     Loss        (%)  100.00   100.00   100.00   100.00
  Uplink     Reorder     (%)    0.00     0.00     0.00     0.00
  Downlink   Loss        (%)    0.00     0.00     0.00     0.00
  Downlink   Reorder     (%)    0.00     0.00     0.00     0.00

━━━  Phase 2 — Bandwidth  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  UDP size   : 1400 B (41 B envelope + 1359 B payload)
  UL/DL dur  : 1 s each
  Interface  : "enp0s3" (10.0.2.15)

  [uplink]  flooding for 1 s …
  [uplink]  sent 8834 pkts

  [downlink] receiving for 1 s …
  Packet size : 1400 bytes (incl. 41 B HMAC envelope)

  Direction   Metric         Min      Max      Avg      Median
  ──────────  ─────────────  ───────  ───────  ───────  ───────
  Uplink     Bandwidth(Mbps)   98.94    98.94    98.94    98.94
  Downlink   Bandwidth(Mbps)    0.00     0.00     0.00     0.00
  Packet size : 1400 bytes (incl. 41 B HMAC envelope)

  Direction   Metric         Min      Max      Avg      Median
  ──────────  ─────────────  ───────  ───────  ───────  ───────
  Uplink     Bandwidth(Mbps)   98.94    98.94    98.94    98.94
  Downlink   Bandwidth(Mbps)    0.00     0.00     0.00     0.00
```