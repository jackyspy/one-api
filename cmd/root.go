package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
)

var (
	cfgFile string
)

var rootCmd = &cobra.Command{
	Use:   "one-api",
	Short: "One API - OpenAI API management and proxy",
	Long: `One API is a unified API gateway that provides a single endpoint to access multiple LLM providers.

It supports three running modes:
  - serve (default): Full API server with web UI and database
  - proxy: Transparent proxy that forwards requests via NATS
  - forwarder: NATS subscriber that processes requests and forwards to LLM APIs`,
	Version: common.Version,
	Run: func(cmd *cobra.Command, args []string) {
		// Default behavior: start full server
		serveCmd.Run(cmd, args)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is $HOME/.one-api.yaml)")
	rootCmd.PersistentFlags().IntP("port", "p", 3000, "listening port")
	rootCmd.PersistentFlags().String("log-dir", "./logs", "log directory")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug mode")
	rootCmd.PersistentFlags().Bool("debug-sql", false, "enable SQL debug mode")

	// Bind flags to viper
	viper.BindPFlag("port", rootCmd.PersistentFlags().Lookup("port"))
	viper.BindPFlag("log-dir", rootCmd.PersistentFlags().Lookup("log-dir"))
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	viper.BindPFlag("debug-sql", rootCmd.PersistentFlags().Lookup("debug-sql"))
}

func setEnvFromViperIfSet(envKey, viperKey string) {
	if !viper.IsSet(viperKey) {
		return
	}
	value := viper.GetString(viperKey)
	if value == "" {
		return
	}
	_ = os.Setenv(envKey, value)
}

func initRuntimeFromViper() {
	// Env vars that are read dynamically by other packages
	setEnvFromViperIfSet("SQL_DSN", "sql-dsn")
	setEnvFromViperIfSet("LOG_SQL_DSN", "log-sql-dsn")
	setEnvFromViperIfSet("SQLITE_PATH", "sqlite-path")
	setEnvFromViperIfSet("SESSION_SECRET", "session-secret")
	setEnvFromViperIfSet("REDIS_CONN_STRING", "redis-conn-string")
	setEnvFromViperIfSet("REDIS_PASSWORD", "redis-password")
	setEnvFromViperIfSet("REDIS_MASTER_NAME", "redis-master-name")
	setEnvFromViperIfSet("SYNC_FREQUENCY", "sync-frequency")
	setEnvFromViperIfSet("NODE_TYPE", "node-type")
	setEnvFromViperIfSet("FRONTEND_BASE_URL", "frontend-base-url")
	setEnvFromViperIfSet("POLLING_INTERVAL", "polling-interval")
	setEnvFromViperIfSet("INITIAL_ROOT_TOKEN", "initial-root-token")
	setEnvFromViperIfSet("INITIAL_ROOT_ACCESS_TOKEN", "initial-root-access-token")

	// Values that were initialized from env at init-time (override in-memory vars so flags work)
	if viper.IsSet("debug") {
		config.DebugEnabled = viper.GetBool("debug")
	}
	if viper.IsSet("debug-sql") {
		config.DebugSQLEnabled = viper.GetBool("debug-sql")
	}
	if viper.IsSet("memory-cache") {
		config.MemoryCacheEnabled = viper.GetBool("memory-cache")
	}
	if viper.IsSet("sync-frequency") && viper.GetInt("sync-frequency") > 0 {
		config.SyncFrequency = viper.GetInt("sync-frequency")
	}
	if viper.IsSet("batch-update-interval") && viper.GetInt("batch-update-interval") > 0 {
		config.BatchUpdateInterval = viper.GetInt("batch-update-interval")
	}
	if viper.IsSet("node-type") {
		config.IsMasterNode = viper.GetString("node-type") != "slave"
	}
	if viper.IsSet("polling-interval") {
		config.RequestInterval = time.Duration(viper.GetInt("polling-interval")) * time.Second
	}
	if viper.IsSet("initial-root-token") {
		config.InitialRootToken = viper.GetString("initial-root-token")
	}
	if viper.IsSet("initial-root-access-token") {
		config.InitialRootAccessToken = viper.GetString("initial-root-access-token")
	}

	// Apply env overrides that used to be done in common.Init()
	if sessionSecret := os.Getenv("SESSION_SECRET"); sessionSecret != "" && sessionSecret != "random_string" {
		config.SessionSecret = sessionSecret
	}
	if sqlitePath := os.Getenv("SQLITE_PATH"); sqlitePath != "" {
		common.SQLitePath = sqlitePath
	}

	// Log dir (was handled by common.Init flag parsing)
	logDir := viper.GetString("log-dir")
	if logDir == "" {
		logDir = "./logs"
	}
	abs, err := filepath.Abs(logDir)
	cobra.CheckErr(err)
	cobra.CheckErr(os.MkdirAll(abs, 0777))
	logger.LogDir = abs
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/one-api/")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".one-api")
	}

	// Read environment variables with ONE_API_ prefix
	viper.SetEnvPrefix("ONE_API")
	viper.AutomaticEnv()

	// Replace - with _ for env variable matching (e.g., nats-url -> NATS_URL)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	// Read config file if exists
	_ = viper.ReadInConfig()
}
