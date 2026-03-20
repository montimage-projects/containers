package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "server":
		runServerCmd(os.Args[2:])
	case "client":
		runClientCmd(os.Args[2:])
	case "version":
		fmt.Printf("netmeasure v%s\n", version)
	default:
		printUsage()
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Server CLI
// ---------------------------------------------------------------------------

func runServerCmd(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	port    := fs.Int("port",    9000,        "UDP port to listen on")
	secret  := fs.String("secret", "changeme", "Shared secret for HMAC-SHA256 authentication")
	verbose := fs.Bool("verbose", false,       "Verbose packet-level logging")
	jsonOut := fs.Bool("json",    false,       "Emit startup confirmation as JSON (then log normally)")
	fs.Parse(args)

	cfg := ServerConfig{
		Port:    *port,
		Key:     deriveKey(*secret),
		Verbose: *verbose,
		JSON:    *jsonOut,
	}
	runServer(cfg)
}

// ---------------------------------------------------------------------------
// Client CLI
// ---------------------------------------------------------------------------

func runClientCmd(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	host        := fs.String("host",        "127.0.0.1", "Server hostname or IP")
	port        := fs.Int("port",           9000,        "Server UDP port")
	iface       := fs.String("iface",       "",          "Network interface to bind (e.g. eth0, en0, bond0)")
	secret      := fs.String("secret",      "changeme",  "Shared secret (must match server)")
	count       := fs.Int("count",          10,          "Number of probe packets")
	intervalMs  := fs.Int("interval",       10,          "Interval between probes (ms)")
	bwDuration  := fs.Int("bw-duration",    1,           "Bandwidth test duration per direction (s)")
	bwPktSize   := fs.Int("bw-pktsize",     1400,        "UDP datagram size for BW test (bytes, incl. envelope)")
	skipProbe   := fs.Bool("skip-probe",    false,       "Skip RTT/loss/reorder phase")
	skipBW      := fs.Bool("skip-bw",       false,       "Skip bandwidth phase")
	verbose     := fs.Bool("verbose",       false,       "Verbose packet-level logging")
	jsonOut     := fs.Bool("json",          false,       "Output results as a single JSON object (suppresses human text)")
	listIfaces  := fs.Bool("list-ifaces",   false,       "List available network interfaces and exit")
	fs.Parse(args)

	if *listIfaces {
		printInterfaces(*jsonOut)
		return
	}

	cfg := ClientConfig{
		Host:       *host,
		Port:       *port,
		Iface:      *iface,
		Key:        deriveKey(*secret),
		Count:      *count,
		IntervalMs: *intervalMs,
		BWDuration: *bwDuration,
		BWPktSize:  *bwPktSize,
		SkipProbe:  *skipProbe,
		SkipBW:     *skipBW,
		Verbose:    *verbose,
		JSON:       *jsonOut,
	}
	runClient(cfg)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func deriveKey(secret string) []byte {
	h := sha256.Sum256([]byte("netmeasure-v1:" + secret))
	return h[:]
}

func printInterfaces(asJSON bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		fatalf("list interfaces: %v", err)
	}

	type ifaceInfo struct {
		Index int    `json:"index"`
		Name  string `json:"name"`
		IP    string `json:"ip"`
		Flags string `json:"flags"`
	}

	var list []ifaceInfo
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		ipStr := ""
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil {
				ipStr = ip4.String()
				break
			}
		}
		if ipStr == "" {
			ipStr = "(no IPv4)"
		}
		list = append(list, ifaceInfo{
			Index: iface.Index,
			Name:  iface.Name,
			IP:    ipStr,
			Flags: iface.Flags.String(),
		})
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(list)
		return
	}

	fmt.Printf("%-6s  %-20s  %-18s  %s\n", "INDEX", "NAME", "IP ADDRESS", "FLAGS")
	fmt.Println("------  --------------------  ------------------  --------------------")
	for _, i := range list {
		fmt.Printf("%-6d  %-20s  %-18s  %s\n", i.Index, i.Name, i.IP, i.Flags)
	}
}

func printBanner(cfg ClientConfig, localIP string) {
	if cfg.JSON {
		return // banner suppressed in JSON mode
	}
	fmt.Printf("╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║     netmeasure v%-5s — CLIENT MODE          ║\n", version)
	fmt.Printf("╚══════════════════════════════════════════════╝\n")
	fmt.Printf("  Target     : %s:%d\n", cfg.Host, cfg.Port)
	if cfg.Iface != "" {
		fmt.Printf("  Interface  : %s (%s)\n", cfg.Iface, localIP)
	} else {
		fmt.Printf("  Interface  : (OS default)\n")
	}
	fmt.Printf("  HMAC auth  : enabled (SHA-256)\n")
	fmt.Printf("  Time       : %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println()
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}

func printUsage() {
	fmt.Printf(`netmeasure v%s — Network Quality Measurement Tool
UDP-only · HMAC-SHA256 authenticated

USAGE:
  netmeasure server  [options]
  netmeasure client  [options]
  netmeasure version

SERVER OPTIONS:
  --port    int     UDP listen port (default 9000)
  --secret  string  Shared HMAC secret (default "changeme")
  --json            Emit startup info as JSON
  --verbose         Verbose packet logging

CLIENT OPTIONS:
  --host        string  Server IP or hostname (default 127.0.0.1)
  --port        int     Server UDP port (default 9000)
  --iface       string  Network interface to bind (e.g. eth0, en0, bond0)
  --secret      string  Shared HMAC secret — must match server
  --count       int     Probe packet count (default 100)
  --interval    int     ms between probes (default 20)
  --bw-duration int     Seconds per BW direction (default 5)
  --bw-pktsize  int     UDP datagram size for BW test in bytes (default 1400)
  --skip-probe       Skip RTT/loss/reorder phase
  --skip-bw          Skip bandwidth phase
  --list-ifaces      List network interfaces and exit
  --json             Output all results as a single JSON object
  --verbose          Verbose logging

METRICS:
  RTT          min / avg / max / jitter (ms)
  Packet loss  uplink %% and downlink %%
  Reordering   uplink %% and downlink %% (RFC 4737)
  Bandwidth    uplink Mbps (server-side) / downlink Mbps (client-side)

SECURITY:
  Every UDP datagram is authenticated with HMAC-SHA256.
  Packets with invalid MACs are silently dropped by the server.
  The shared secret is hashed (SHA-256, domain-separated) to produce
  the 32-byte key — the raw secret is never used directly.

EXAMPLES:
  netmeasure server --port 9000 --secret mysecret
  netmeasure server --port 9000 --secret mysecret --json

  netmeasure client --host 10.0.0.1 --secret mysecret
  netmeasure client --host 10.0.0.1 --secret mysecret --iface eth1
  netmeasure client --host 10.0.0.1 --secret mysecret --iface bond0 --count 500 --interval 10
  netmeasure client --host 10.0.0.1 --secret mysecret --json
  netmeasure client --list-ifaces --json

`, version)
}
