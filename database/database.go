package database

import (
	"fmt"
	"os"
	"people/logging"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	C    *gorm.DB
	Mock sqlmock.Sqlmock
	log  = logging.Config
)

// The function initializes the connection data from the environment
// variables, performs a database connection, otherwise return an error
// with the program shutdown.
func Connect() {
	f := logging.F()
	var err error
	host := os.Getenv("DB_HOST")
	user := os.Getenv("DB_USER")
	pass := os.Getenv("DB_PASSWORD")
	dbMain := os.Getenv("DB_MAIN")
	//dbTest := os.Getenv("DB_TEST")
	port := os.Getenv("DB_PORT")
	log.Infof("Gin running mode: %v", gin.Mode())
	if gin.Mode() == gin.TestMode {
		sqlDB, Mock, err := sqlmock.New()
		if err != nil {
			log.Fatal(f+"failed to initialize mock connection:", err)
		}
		_ = Mock
		C, err = gorm.Open(postgres.New(postgres.Config{
			Conn: sqlDB,
		}), &gorm.Config{Logger: logging.GL(log)})
		if err != nil {
			log.Fatal(f+"failed to initialize test database:", err)
		}
	} else {
		dsn := fmt.Sprintf(
			"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable",
			host, user, pass, dbMain, port,
		)
		C, err = gorm.Open(
			postgres.Open(dsn),
			&gorm.Config{Logger: logging.GL(log)},
		)
		log.Infof("Working with %s database...", dbMain)
		if err != nil {
			log.Fatal(f+"failed to initialize main database:", err)
		}
	}
}
