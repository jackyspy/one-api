package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/i18n"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/pkg/datapipe"
	_ "github.com/songquanpeng/one-api/pkg/datapipe/nats" // register nats DataPipe
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/router"
)

var forwarderCmd = &cobra.Command{
	Use:   "forwarder",
	Short: "Start NATS forwarder mode",
	Long: `Start One API in forwarder mode.

In this mode, One API:
- Subscribes to NATS message queue for incoming requests
- Processes requests using full One API logic (database, routing, etc.)
- Forwards requests to actual LLM APIs based on channel configuration

This mode requires database connection for channel and model configuration.`,
	Run: runForwarder,
}

func init() {
	rootCmd.AddCommand(forwarderCmd)

	// Forwarder-specific flags
	forwarderCmd.Flags().String("nats-url", "", "NATS server URL (required)")
	forwarderCmd.Flags().String("nats-subject", "", "NATS subject to subscribe (required)")
	forwarderCmd.Flags().String("nats-queue", "", "NATS queue group (optional, for multiple forwarders)")
	forwarderCmd.Flags().Int("internal-port", 13000, "internal relay service port")
	forwarderCmd.Flags().String("sql-dsn", "", "database connection string")
	forwarderCmd.Flags().String("log-sql-dsn", "", "secondary log database DSN")
	forwarderCmd.Flags().String("redis-conn-string", "", "Redis connection string")
	forwarderCmd.Flags().Int("sync-frequency", 600, "sync frequency in seconds")
	forwarderCmd.Flags().Bool("memory-cache", false, "enable memory cache")
	forwarderCmd.Flags().Bool("batch-update", false, "enable batch update")
	forwarderCmd.Flags().Int("batch-update-interval", 5, "batch update interval in seconds")
	forwarderCmd.Flags().String("gin-mode", "release", "Gin mode (debug/release)")

	// Bind flags to viper
	viper.BindPFlag("nats-url", forwarderCmd.Flags().Lookup("nats-url"))
	viper.BindPFlag("nats-subject", forwarderCmd.Flags().Lookup("nats-subject"))
	viper.BindPFlag("nats-queue", forwarderCmd.Flags().Lookup("nats-queue"))
	viper.BindPFlag("internal-port", forwarderCmd.Flags().Lookup("internal-port"))
	viper.BindPFlag("sql-dsn", forwarderCmd.Flags().Lookup("sql-dsn"))
	viper.BindPFlag("log-sql-dsn", forwarderCmd.Flags().Lookup("log-sql-dsn"))
	viper.BindPFlag("redis-conn-string", forwarderCmd.Flags().Lookup("redis-conn-string"))
	viper.BindPFlag("sync-frequency", forwarderCmd.Flags().Lookup("sync-frequency"))
	viper.BindPFlag("memory-cache", forwarderCmd.Flags().Lookup("memory-cache"))
	viper.BindPFlag("batch-update", forwarderCmd.Flags().Lookup("batch-update"))
	viper.BindPFlag("batch-update-interval", forwarderCmd.Flags().Lookup("batch-update-interval"))
	viper.BindPFlag("gin-mode", forwarderCmd.Flags().Lookup("gin-mode"))

	// Set env variable bindings
	viper.BindEnv("nats-url", "NATS_URL")
	viper.BindEnv("nats-subject", "NATS_SUBJECT")
	viper.BindEnv("nats-queue", "NATS_QUEUE")
	viper.BindEnv("internal-port", "FORWARDER_INTERNAL_PORT")
	viper.BindEnv("sql-dsn", "SQL_DSN")
	viper.BindEnv("log-sql-dsn", "LOG_SQL_DSN")
	viper.BindEnv("redis-conn-string", "REDIS_CONN_STRING")
	viper.BindEnv("sync-frequency", "SYNC_FREQUENCY")
	viper.BindEnv("memory-cache", "MEMORY_CACHE_ENABLED")
	viper.BindEnv("batch-update", "BATCH_UPDATE_ENABLED")
	viper.BindEnv("batch-update-interval", "BATCH_UPDATE_INTERVAL")
	viper.BindEnv("gin-mode", "GIN_MODE")
}

func runForwarder(cmd *cobra.Command, args []string) {
	fmt.Println("Starting One API in forwarder mode...")

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
		fmt.Println("FATAL: --nats-subject is required (or set NATS_SUBJECT environment variable)")
		os.Exit(1)
	}

	natsQueue, _ := cmd.Flags().GetString("nats-queue")
	if natsQueue == "" {
		natsQueue = viper.GetString("nats-queue")
	}

	initRuntimeFromViper()
	logger.SetupLogger()
	logger.SysLogf("One API Forwarder %s started", common.Version)

	if viper.GetString("gin-mode") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}
	if config.DebugEnabled {
		logger.SysLog("running in debug mode")
	}

	// Initialize SQL Database (needed for channel/model config)
	model.InitDB()
	model.InitLogDB()

	var err error
	err = model.CreateRootAccountIfNeed()
	if err != nil {
		logger.FatalLog("database init error: " + err.Error())
	}
	defer func() {
		err := model.CloseDB()
		if err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
	}()

	// Initialize Redis (optional, for caching)
	err = common.InitRedisClient()
	if err != nil {
		logger.SysLog("Redis not available, continuing without cache: " + err.Error())
	}

	// Initialize options
	model.InitOptionMap()
	if common.RedisEnabled {
		config.MemoryCacheEnabled = true
	}
	if config.MemoryCacheEnabled {
		logger.SysLog("memory cache enabled")
		logger.SysLog(fmt.Sprintf("sync frequency: %d seconds", config.SyncFrequency))
		model.InitChannelCache()
		go model.SyncOptions(config.SyncFrequency)
		go model.SyncChannelCache(config.SyncFrequency)
	}

	if viper.GetBool("batch-update") {
		config.BatchUpdateEnabled = true
		logger.SysLog("batch update enabled with interval " + strconv.Itoa(config.BatchUpdateInterval) + "s")
		model.InitBatchUpdater()
	}

	if config.EnableMetric {
		logger.SysLog("metric enabled, will disable channel if too much request failed")
	}

	openai.InitTokenEncoders()
	client.Init()

	// Initialize i18n
	if err := i18n.Init(); err != nil {
		logger.FatalLog("failed to initialize i18n: " + err.Error())
	}

	// Start internal HTTP server for relay processing
	internalPort := strconv.Itoa(viper.GetInt("internal-port"))
	if internalPort == "0" {
		internalPort = "13000"
	}

	internalServer := gin.New()
	internalServer.Use(gin.Recovery())
	internalServer.Use(middleware.RequestId())
	internalServer.Use(middleware.Language())
	middleware.SetUpLogger(internalServer)

	// Use session store (required by some middleware)
	store := cookie.NewStore([]byte(config.SessionSecret))
	internalServer.Use(sessions.Sessions("session", store))

	// Relay routes only (no dashboard, no web UI)
	router.SetRelayRouter(internalServer)

	go func() {
		logger.SysLogf("Forwarder internal relay server started on http://127.0.0.1:%s", internalPort)
		if err := internalServer.Run("127.0.0.1:" + internalPort); err != nil {
			logger.FatalLog("failed to start internal server: " + err.Error())
		}
	}()

	// Get the NATS DataPipe plugin
	pipe := datapipe.Get("nats")
	if pipe == nil {
		fmt.Println("FATAL: NATS DataPipe plugin not found")
		os.Exit(1)
	}

	// Initialize the plugin with internal port for forwarding
	pipeConfig := map[string]string{
		"nats_url":     natsURL,
		"nats_subject": natsSubject,
		"internal_url": "http://127.0.0.1:" + internalPort,
	}
	if natsQueue != "" {
		pipeConfig["nats_queue"] = natsQueue
	}

	if err := pipe.Init(pipeConfig); err != nil {
		fmt.Printf("FATAL: Failed to initialize NATS plugin: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Forwarder configured:\n")
	fmt.Printf("  NATS URL: %s\n", natsURL)
	fmt.Printf("  NATS Subject: %s\n", natsSubject)
	if natsQueue != "" {
		fmt.Printf("  NATS Queue: %s\n", natsQueue)
	}
	fmt.Printf("  Internal Relay: http://127.0.0.1:%s\n", internalPort)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down forwarder...")
		os.Exit(0)
	}()

	// Start listening on NATS (this blocks)
	fmt.Println("Forwarder is now running. Press Ctrl+C to stop.")
	if err := pipe.Listen(""); err != nil {
		fmt.Printf("FATAL: Listen failed: %v\n", err)
		os.Exit(1)
	}
}
