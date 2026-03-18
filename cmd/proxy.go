package cmd

import (
	 "bytes"
    "errors"
    "fmt"
    "io"
    "net/http"
    "os"
    "os/signal"
    "strconv"
    "syscall"

    "github.com/gin-gonic/gin"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"

    "github.com/songquanpeng/one-api/common"
    "github.com/songquanpeng/one-api/common/logger"
    "github.com/songquanpeng/one-api/middleware"
    "github.com/songquanpeng/one-api/pkg/datapipe"
    _ "github.com/songquanpeng/one-api/pkg/datapipe/nats" // register nats DataPipe
)

var proxyCmd = &cobra.Command{
    Use:   "proxy",
    Short: "Start transparent proxy mode",
    Long: `Start One API in transparent proxy mode.

In this mode, One API acts as a pure transparent proxy that:
- Receives HTTP requests from agents
- Forwards requests through NATS message queue
- Returns responses back to clients

No database connection is required. All routing logic is handled by the forwarder.`,
    Run: runProxy,
}

func init() {
    rootCmd.AddCommand(proxyCmd)

    // Proxy-specific flags
    proxyCmd.Flags().String("nats-url", "", "NATS server URL (required)")
    proxyCmd.Flags().String("nats-subject", "llm.requests", "NATS subject for requests")
    proxyCmd.Flags().Int64("max-body-bytes", 524288, "max request body size forwarded through NATS (bytes)")
    proxyCmd.Flags().String("gin-mode", "release", "Gin mode (debug/release)")
    // TLS flags
    proxyCmd.Flags().Bool("nats-tls", false, "enable NATS TLS")
    proxyCmd.Flags().Bool("nats-tls-skip-verify", false, "skip TLS certificate verification")
    // Bind flags to viper
    viper.BindPFlag("nats-url", proxyCmd.Flags().Lookup("nats-url"))
    viper.BindPFlag("nats-subject", proxyCmd.Flags().Lookup("nats-subject"))
    viper.BindPFlag("max-body-bytes", proxyCmd.Flags().Lookup("max-body-bytes"))
    viper.BindPFlag("gin-mode", proxyCmd.Flags().Lookup("gin-mode"))
    viper.BindPFlag("nats-tls", proxyCmd.Flags().Lookup("nats-tls"))
    viper.BindPFlag("nats-tls-skip-verify", proxyCmd.Flags().Lookup("nats-tls-skip-verify"))
    // Set env variable bindings
    viper.BindEnv("nats-url", "NATS_URL")
    viper.BindEnv("nats-subject", "NATS_SUBJECT")
    viper.BindEnv("max-body-bytes", "NATS_MAX_BODY_BYTES")
    viper.BindEnv("gin-mode", "GIN_MODE")
    viper.BindEnv("nats-tls", "NATS_TLS")
    viper.BindEnv("nats-tls-skip-verify", "NATS_TLS_SKIP_VERIFY")
    viper.BindEnv("port", "PORT")
}

func runProxy(cmd *cobra.Command, args []string) {
    fmt.Println("Starting One API in proxy mode (transparent pass-through)...")

    // Get flag values directly from command (CLI flags take precedence)
    natsURL, _ := cmd.Flags().GetString("nats-url")
    if natsURL == "" {
        natsURL = viper.GetString("nats-url")
    }
    if natsURL == "" {
        fmt.Println("FATAL: --nats-url is required (or set NATS_URL environment variable)")
        os.Exit(1)
    }

    natsSubject, _ := cmd.Flags().GetString("nats-subject")
    if natsSubject == "" {
        natsSubject = viper.GetString("nats-subject")
    }
    if natsSubject == "" {
        natsSubject = "llm.requests" // default value
    }

    maxBodyBytes := viper.GetInt64("max-body-bytes")
    if maxBodyBytes <= 0 {
        maxBodyBytes = 0 * 1024 * 1024 // 1MB default
    }

    initRuntimeFromViper()
    logger.SetupLogger()
    logger.SysLogf("One API Proxy %s started", common.Version)
    if viper.GetString("gin-mode") != gin.DebugMode {
        gin.SetMode(gin.ReleaseMode)
    }
    // Get the NATS DataPipe plugin
    pipe := datapipe.Get("nats")
    if pipe == nil {
        fmt.Println("FATAL: NATS DataPipe plugin not found")
        os.Exit(1)
    }
    // Build pipe config
    pipeConfig := map[string]string{
        "nats_url":     natsURL,
        "nats_subject": natsSubject,
        "max_body_bytes": strconv.FormatInt(maxBodyBytes),
    }
    // Handle TLS configuration
    if viper.GetBool("nats-tls") {
        pipeConfig["nats_tls"] = "true"
    }
    if viper.GetBool("nats-tls-skip-verify") {
        pipeConfig["nats_tls_skip_verify"] = "true"
    }
    if err := pipe.Init(pipeConfig); err != nil {
        fmt.Printf("FATAL: Failed to initialize NATS plugin: %v\n", err)
        os.Exit(1)
    }
    fmt.Printf("Proxy configured:\n")
    fmt.Printf("  NATS URL: %s\n", natsURL)
    fmt.Printf("  NATS Subject: %s\n", natsSubject)
    fmt.Printf("  Max Body: %d bytes\n", maxBodyBytes)
    // Create HTTP transport that uses DataPipe
    httpClient := &http.Client{Transport: pipe.GetTransport()}
    // Initialize HTTP server
    server := gin.New()
    server.Use(gin.Recovery())
    server.Use(middleware.RequestId())
    server.Use(middleware.Language())
    // Setup transparent proxy routes
    setupProxyRoutes(server, httpClient)
    port := strconv.Itoa(viper.GetInt("port"))
    if port == "0"{
        port = "3000"
    }
    fmt.Printf("Proxy server started on http://0.0.0.0:%s\n", port)
    fmt.Println("All requests will be forwarded through NATS to the forwarder")
    // Setup signal handling for graceful shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        fmt.Println("\nShutting down proxy...")
        os.Exit(0)
    }()
    if err := server.Run(":" + port); err != nil {
        fmt.Printf("FATAL: Failed to start server: %v\n", err)
        os.Exit(1)
    }
}
func setupProxyRoutes(r *gin.Engine, httpClient *http.Client) {
    r.Use(middleware.CORS())
    r.Use(middleware.GzipDecodeMiddleware())
    // Handler for all /v1/* routes
    proxyHandler := func(c *gin.Context) {
        // Build target URL using nats:// scheme with configured subject
        natsSubject := viper.GetString("nats-subject")
        targetURL := fmt.Sprintf("nats://%s%s?%s", natsSubject, c.Request.URL.Path)
        if c.Request.URL.RawQuery != "" {
            targetURL += "?" + c.Request.URL.RawQuery
        }
        // Read request body
        var bodyBytes []byte
        var err error
        if c.Request.Body != nil {
            bodyBytes, err = io.ReadAll(c.Request.Body)
            if err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request body"})
                return
            }
            _ = c.Request.Body.Close()
        }
        // Create new request to forward
        proxyReq, err := http.NewRequest(c.Request.Method, targetURL, bytes.NewReader(bodyBytes))
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create proxy request"})
            return
        }
        // Copy headers
        for key, values := range c.Request.Header {
            for _, value := range values {
                proxyReq.Header.Add(key, value)
            }
        }
        // Forward request through DataPipe
        resp, err := httpClient.Do(proxyReq)
        if err != nil {
            if errors.Is(err, datapipe.ErrBodyTooLarge) {
                c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
                return
            }
            c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("proxy request failed: %v", err)})
            return
        }
        defer resp.Body.Close()
        // Copy response headers
        for key, values := range resp.Header {
            for _, value := range values {
                c.Writer.Header().Add(key, value)
            }
        }
        // Set status code
        c.Writer.WriteHeader(resp.StatusCode)
        // Stream response body
        _, err = io.Copy(c.Writer, resp.Body)
        if err != nil {
            fmt.Printf("[Proxy] Error streaming response: %v\n", err)
        }
    }
    // Setup routes - all /v1/* paths go through the proxy handler
    v1 := r.Group("/v1")
    {
        v1.Any("/*path", proxyHandler)
    }
    // Health check endpoint
    r.GET("/health", func(c *gin.Context) {
        c.JSON(http.StatusOK, gin.H{"status": "ok", "mode": "proxy"})
    })
}
