package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Version and metadata
const AppName = "Gemini-IPv6-Proxy"

// Log levels
const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Global state
var (
	config      Config
	logger      *LeveledLogger
	localSubnet *net.IPNet
	validIPv6s  []string
	mu          sync.RWMutex // Protects validIPv6s
	keyRegex    = regexp.MustCompile(`(?i)(key|api_key)=([^&]+)`)
)

// Config holds application settings
type Config struct {
	TargetHost     string
	ListenAddr     string
	IPv6ListURL    string
	UpdateInterval time.Duration
	ManualCIDR     string
	LogLevel       string
	LogFile        string
}

// LeveledLogger provides basic leveled logging
type LeveledLogger struct {
	level  int
	logger *log.Logger
}

func main() {
	parseConfig()
	setupLogger()

	// 1. Network Initialization
	if err := initLocalSubnet(); err != nil {
		logger.Fatalf("Failed to init local subnet: %v", err)
	}

	// 2. Initial IP List Fetch
	if err := fetchAndReloadIPs(); err != nil {
		logger.Warnf("Initial IP fetch failed: %v", err)
	}
	go ipUpdaterLoop()

	// 3. Reverse Proxy Setup
	targetURL := &url.URL{Scheme: "https", Host: config.TargetHost}
	
	proxy := &httputil.ReverseProxy{
		Transport:     newTransport(),
		FlushInterval: -1, // Disable buffering for streaming support
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host
			req.Header.Del("X-Forwarded-For")
			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "")
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				logger.Errorf("Proxy error: %v", err)
			}
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	// 4. Start Server
	server := &http.Server{
		Addr:    config.ListenAddr,
		Handler: logMiddleware(proxy),
	}

	fmt.Printf("%s started on %s (Level: %s)\n", AppName, config.ListenAddr, config.LogLevel)
	if err := server.ListenAndServe(); err != nil {
		logger.Fatalf("Server failed: %v", err)
	}
}

// newTransport creates a high-performance transport with IPv6 rotation
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2000,
		MaxIdleConnsPerHost:   1000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: config.TargetHost,
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Only intercept traffic to the target host
			if strings.Contains(addr, config.TargetHost) {
				return dialCustom(ctx)
			}
			// Fallback for other hosts
			d := net.Dialer{Timeout: 30 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
}

// dialCustom handles the core IPv6 rotation logic (Src & Dest)
func dialCustom(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	// Bind random source IPv6
	if srcIP := genRandomIPv6(localSubnet); srcIP != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: srcIP}
	}

	// Pick random destination IPv6
	destIP := pickRandomDestIP()
	if destIP == "" {
		// Fallback to DNS resolution if list is empty
		return dialer.DialContext(ctx, "tcp6", net.JoinHostPort(config.TargetHost, "443"))
	}

	if logger.level <= LevelDebug {
		src := "System"
		if dialer.LocalAddr != nil {
			src = dialer.LocalAddr.String()
		}
		logger.Debugf("Dial: %s -> %s", src, destIP)
	}

	// Force IPv6 connection via IP to bypass DNS
	conn, err := dialer.DialContext(ctx, "tcp6", net.JoinHostPort(destIP, "443"))
	if err != nil {
		logger.Warnf("Dial failed to %s: %v", destIP, err)
		return nil, err
	}
	return conn, nil
}

// ipUpdaterLoop runs in background to refresh valid IPs
func ipUpdaterLoop() {
	ticker := time.NewTicker(config.UpdateInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := fetchAndReloadIPs(); err != nil {
			logger.Warnf("IP update failed: %v", err)
		} else {
			logger.Debugf("IP list updated")
		}
	}
}

func fetchAndReloadIPs() error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(config.IPv6ListURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("http status: %d", resp.StatusCode)
	}

	var tempIPs []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Validate IPv6
		if ip := net.ParseIP(line); ip != nil && ip.To4() == nil {
			tempIPs = append(tempIPs, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read error: %v", err)
	}
	if len(tempIPs) == 0 {
		return fmt.Errorf("empty valid IP list")
	}

	mu.Lock()
	validIPv6s = tempIPs
	count := len(validIPv6s)
	mu.Unlock()

	logger.Infof("Loaded %d IPv6 addresses", count)
	return nil
}

// genRandomIPv6 generates a random IP within the subnet: (Prefix & Mask) | (Random & ^Mask)
func genRandomIPv6(network *net.IPNet) net.IP {
	if network == nil {
		return nil
	}
	netIP := network.IP.To16()
	mask := network.Mask
	if netIP == nil || len(mask) != 16 {
		return nil
	}

	randBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, randBytes); err != nil {
		return nil
	}

	finalIP := make(net.IP, 16)
	for i := 0; i < 16; i++ {
		finalIP[i] = (netIP[i] & mask[i]) | (randBytes[i] & ^mask[i])
	}
	return finalIP
}

func pickRandomDestIP() string {
	mu.RLock()
	defer mu.RUnlock()
	if len(validIPv6s) == 0 {
		return ""
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(validIPv6s))))
	if err != nil {
		return validIPv6s[0]
	}
	return validIPv6s[n.Int64()]
}

func initLocalSubnet() error {
	cidr := config.ManualCIDR
	
	// Auto-detect if not provided
	if cidr == "" {
		cmd := exec.Command("sh", "-c", "ip -6 route show table local | grep -v '^fe80' | grep '/' | head -n 1")
		out, _ := cmd.Output()
		fields := strings.Fields(string(out))
		for _, f := range fields {
			if strings.Contains(f, "/") {
				cidr = f
				break
			}
		}
		if cidr != "" {
			logger.Infof("Auto-detected subnet: %s", cidr)
		}
	}

	if cidr == "" {
		return fmt.Errorf("no subnet detected, use -cidr")
	}

	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %v", err)
	}
	localSubnet = network
	return nil
}

// logMiddleware logs requests and redacts sensitive keys
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logger.level > LevelInfo {
			next.ServeHTTP(w, r)
			return
		}
		
		start := time.Now()
		ww := &responseWrapper{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(ww, r)
		
		safeURL := keyRegex.ReplaceAllString(r.URL.String(), "$1=[REDACTED]")
		logger.Infof("[%d] %s %s | %s | %v", ww.statusCode, r.Method, safeURL, r.RemoteAddr, time.Since(start))
	})
}

// Helper: Config Parsing
func parseConfig() {
	// Defaults
	config = Config{
		TargetHost:     "generativelanguage.googleapis.com",
		ListenAddr:     ":8080",
		IPv6ListURL:    "https://raw.githubusercontent.com/ccbkkb/ipv6-googleapis/refs/heads/main/valid_ips.txt",
		UpdateInterval: 1 * time.Hour,
		LogLevel:       "ERROR",
	}

	// Environment overrides
	if v := os.Getenv("TARGET_HOST"); v != "" { config.TargetHost = v }
	if v := os.Getenv("LISTEN_ADDR"); v != "" { config.ListenAddr = v }
	if v := os.Getenv("IPV6_CIDR"); v != "" { config.ManualCIDR = v }
	if v := os.Getenv("LOG_LEVEL"); v != "" { config.LogLevel = v }
	if v := os.Getenv("LOG_FILE"); v != "" { config.LogFile = v }

	// Flags overrides
	flag.StringVar(&config.ListenAddr, "listen", config.ListenAddr, "Address to listen on")
	flag.StringVar(&config.ManualCIDR, "cidr", config.ManualCIDR, "Manual IPv6 CIDR (e.g. 2001:db8::/48)")
	flag.StringVar(&config.LogLevel, "log-level", config.LogLevel, "Log level: DEBUG, INFO, WARN, ERROR")
	flag.StringVar(&config.LogFile, "log-file", config.LogFile, "Path to log file")
	flag.Parse()
}

// Helper: Logger Setup
func setupLogger() {
	lvl := LevelError
	switch strings.ToUpper(config.LogLevel) {
	case "DEBUG": lvl = LevelDebug
	case "INFO":  lvl = LevelInfo
	case "WARN":  lvl = LevelWarn
	}

	var w io.Writer = os.Stdout
	if config.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(config.LogFile), 0755); err == nil {
			if f, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				w = io.MultiWriter(os.Stdout, f)
			}
		}
	}

	logger = &LeveledLogger{
		level:  lvl,
		logger: log.New(w, "", log.LstdFlags|log.Lmicroseconds),
	}
}

// Helper: Logging Methods
func (l *LeveledLogger) Debugf(f string, v ...interface{}) { if l.level <= LevelDebug { l.logger.Printf("[DEBG] "+f, v...) } }
func (l *LeveledLogger) Infof(f string, v ...interface{})  { if l.level <= LevelInfo  { l.logger.Printf("[INFO] "+f, v...) } }
func (l *LeveledLogger) Warnf(f string, v ...interface{})  { if l.level <= LevelWarn  { l.logger.Printf("[WARN] "+f, v...) } }
func (l *LeveledLogger) Errorf(f string, v ...interface{}) { if l.level <= LevelError { l.logger.Printf("[ERRO] "+f, v...) } }
func (l *LeveledLogger) Fatalf(f string, v ...interface{}) { l.logger.Printf("[FATL] "+f, v...); os.Exit(1) }

// Helper: Response Wrapper
type responseWrapper struct {
	http.ResponseWriter
	statusCode int
}
func (rw *responseWrapper) WriteHeader(code int) { rw.statusCode = code; rw.ResponseWriter.WriteHeader(code) }
func (rw *responseWrapper) Flush() { if f, ok := rw.ResponseWriter.(http.Flusher); ok { f.Flush() } }
