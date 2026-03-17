package cmd

import (
	"embed"
	"fmt"
	"strconv"

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
	"github.com/songquanpeng/one-api/controller"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/router"
)

// BuildFS holds the embedded frontend files (set by main package)
var BuildFS embed.FS

// SetBuildFS allows main package to set the embedded file system
func SetBuildFS(fs embed.FS) {
	BuildFS = fs
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start API server (default mode)",
	Long: `Start the One API server with full functionality including:
- Web management interface
- Token and channel management
- Database storage
- Full API gateway features`,
	Run: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	// Serve-specific flags
	serveCmd.Flags().String("sql-dsn", "", "database connection string (MySQL/PostgreSQL/SQLite)")
	serveCmd.Flags().String("log-sql-dsn", "", "secondary log database DSN")
	serveCmd.Flags().String("sqlite-path", "", "SQLite database path")
	serveCmd.Flags().String("session-secret", "", "session secret key")
	serveCmd.Flags().String("redis-conn-string", "", "Redis connection string")
	serveCmd.Flags().String("redis-password", "", "Redis password (cluster mode)")
	serveCmd.Flags().String("redis-master-name", "", "Redis master name (cluster mode)")
	serveCmd.Flags().Int("sync-frequency", 600, "sync frequency in seconds")
	serveCmd.Flags().Bool("memory-cache", false, "enable memory cache")
	serveCmd.Flags().Bool("batch-update", false, "enable batch update")
	serveCmd.Flags().Int("batch-update-interval", 5, "batch update interval in seconds")
	serveCmd.Flags().Int("channel-test-frequency", 0, "channel test frequency in seconds")
	serveCmd.Flags().String("gin-mode", "release", "Gin mode (debug/release)")
	serveCmd.Flags().String("frontend-base-url", "", "frontend base URL")
	serveCmd.Flags().String("node-type", "master", "node type (master/slave)")
	serveCmd.Flags().Int("polling-interval", 0, "polling interval in seconds")
	serveCmd.Flags().String("initial-root-token", "", "initial root token")
	serveCmd.Flags().String("initial-root-access-token", "", "initial root access token")

	// Bind flags to viper
	viper.BindPFlag("sql-dsn", serveCmd.Flags().Lookup("sql-dsn"))
	viper.BindPFlag("log-sql-dsn", serveCmd.Flags().Lookup("log-sql-dsn"))
	viper.BindPFlag("sqlite-path", serveCmd.Flags().Lookup("sqlite-path"))
	viper.BindPFlag("session-secret", serveCmd.Flags().Lookup("session-secret"))
	viper.BindPFlag("redis-conn-string", serveCmd.Flags().Lookup("redis-conn-string"))
	viper.BindPFlag("redis-password", serveCmd.Flags().Lookup("redis-password"))
	viper.BindPFlag("redis-master-name", serveCmd.Flags().Lookup("redis-master-name"))
	viper.BindPFlag("sync-frequency", serveCmd.Flags().Lookup("sync-frequency"))
	viper.BindPFlag("memory-cache", serveCmd.Flags().Lookup("memory-cache"))
	viper.BindPFlag("batch-update", serveCmd.Flags().Lookup("batch-update"))
	viper.BindPFlag("batch-update-interval", serveCmd.Flags().Lookup("batch-update-interval"))
	viper.BindPFlag("channel-test-frequency", serveCmd.Flags().Lookup("channel-test-frequency"))
	viper.BindPFlag("gin-mode", serveCmd.Flags().Lookup("gin-mode"))
	viper.BindPFlag("frontend-base-url", serveCmd.Flags().Lookup("frontend-base-url"))
	viper.BindPFlag("node-type", serveCmd.Flags().Lookup("node-type"))
	viper.BindPFlag("polling-interval", serveCmd.Flags().Lookup("polling-interval"))
	viper.BindPFlag("initial-root-token", serveCmd.Flags().Lookup("initial-root-token"))
	viper.BindPFlag("initial-root-access-token", serveCmd.Flags().Lookup("initial-root-access-token"))

	// Set env variable bindings (for backward compatibility)
	viper.BindEnv("sql-dsn", "SQL_DSN")
	viper.BindEnv("log-sql-dsn", "LOG_SQL_DSN")
	viper.BindEnv("sqlite-path", "SQLITE_PATH")
	viper.BindEnv("session-secret", "SESSION_SECRET")
	viper.BindEnv("redis-conn-string", "REDIS_CONN_STRING")
	viper.BindEnv("redis-password", "REDIS_PASSWORD")
	viper.BindEnv("redis-master-name", "REDIS_MASTER_NAME")
	viper.BindEnv("sync-frequency", "SYNC_FREQUENCY")
	viper.BindEnv("memory-cache", "MEMORY_CACHE_ENABLED")
	viper.BindEnv("batch-update", "BATCH_UPDATE_ENABLED")
	viper.BindEnv("batch-update-interval", "BATCH_UPDATE_INTERVAL")
	viper.BindEnv("channel-test-frequency", "CHANNEL_TEST_FREQUENCY")
	viper.BindEnv("gin-mode", "GIN_MODE")
	viper.BindEnv("frontend-base-url", "FRONTEND_BASE_URL")
	viper.BindEnv("node-type", "NODE_TYPE")
	viper.BindEnv("polling-interval", "POLLING_INTERVAL")
	viper.BindEnv("initial-root-token", "INITIAL_ROOT_TOKEN")
	viper.BindEnv("initial-root-access-token", "INITIAL_ROOT_ACCESS_TOKEN")
	viper.BindEnv("port", "PORT")
	viper.BindEnv("debug", "DEBUG")
	viper.BindEnv("debug-sql", "DEBUG_SQL")
}

func runServe(cmd *cobra.Command, args []string) {
	initRuntimeFromViper()
	logger.SetupLogger()
	logger.SysLogf("One API %s started", common.Version)

	if viper.GetString("gin-mode") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}
	if config.DebugEnabled {
		logger.SysLog("running in debug mode")
	}

	// Initialize SQL Database
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

	// Initialize Redis
	err = common.InitRedisClient()
	if err != nil {
		logger.FatalLog("failed to initialize Redis: " + err.Error())
	}

	// Initialize options
	model.InitOptionMap()
	logger.SysLog(fmt.Sprintf("using theme %s", config.Theme))
	if common.RedisEnabled {
		config.MemoryCacheEnabled = true
	}
	if config.MemoryCacheEnabled {
		logger.SysLog("memory cache enabled")
		logger.SysLog(fmt.Sprintf("sync frequency: %d seconds", config.SyncFrequency))
		model.InitChannelCache()
	}
	if config.MemoryCacheEnabled {
		go model.SyncOptions(config.SyncFrequency)
		go model.SyncChannelCache(config.SyncFrequency)
	}
	channelTestFreq := viper.GetInt("channel-test-frequency")
	if channelTestFreq > 0 {
		go controller.AutomaticallyTestChannels(channelTestFreq)
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

	// Initialize HTTP server
	server := gin.New()
	server.Use(gin.Recovery())
	server.Use(middleware.RequestId())
	server.Use(middleware.Language())
	middleware.SetUpLogger(server)
	store := cookie.NewStore([]byte(config.SessionSecret))
	server.Use(sessions.Sessions("session", store))

	router.SetRouter(server, BuildFS)
	port := strconv.Itoa(viper.GetInt("port"))
	logger.SysLogf("server started on http://localhost:%s", port)
	err = server.Run(":" + port)
	if err != nil {
		logger.FatalLog("failed to start HTTP server: " + err.Error())
	}
}
