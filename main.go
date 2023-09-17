package main

import (
	"os"
	db "people/database"
	"people/handlers"
	"people/kafka"
	"people/logging"
	"people/models"

	"github.com/gin-gonic/contrib/secure"
	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"

	"github.com/sirupsen/logrus"
)

var (
	log      = logging.Config
	security = secure.Options{
		AllowedHosts:          []string{"127.0.0.1:8080", "example.com:443"},
		SSLRedirect:           false, // true if not behind nginx
		SSLHost:               "example.com:443",
		SSLProxyHeaders:       map[string]string{"X-Forwarded-Proto": "http"},
		STSSeconds:            315360000,
		STSIncludeSubdomains:  true,
		FrameDeny:             true,
		ContentTypeNosniff:    true,
		BrowserXssFilter:      true,
		ContentSecurityPolicy: "default-src 'self'",
	}
)

func main() {
	// Connect to database
	db.Connect()
	db.C.AutoMigrate(&models.Entry{})

	// Run Kafka
	topics := kafka.Topics{
		{Name: os.Getenv("DATA"), Partitions: 1, Replication: 1},
		{Name: os.Getenv("FAIL"), Partitions: 1, Replication: 1},
	}
	kafka.Start(topics)
	dataTopic := topics[0]
	failTopic := topics[1]
	go handlers.GetMsg(dataTopic, failTopic)

	// Run router
	r := router()
	r.Run("127.0.0.1:8080")
}

func router() *gin.Engine {
	// Gin settings
	r := gin.New()
	r.SetTrustedProxies([]string{"127.0.0.1"})
	r.Use(gin.LoggerWithWriter(log.WriterLevel(logrus.InfoLevel)))
	r.Use(gin.RecoveryWithWriter(log.WriterLevel(logrus.ErrorLevel)))
	r.Use(secure.Secure(security))

	// Routes
	api := r.Group("/api")
	api.POST("/create", handlers.Create)
	api.GET("/read", handlers.Read)
	api.PATCH("/update", handlers.Update)
	api.DELETE("/delete", handlers.Delete)
	r.POST("/graphql", handlers.GraphQL)
	return r
}
