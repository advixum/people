package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	db "people/database"
	"people/handlers"
	"people/kafka"
	"people/models"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// Requirements: .env PostgreSQL, Apache Kafka, Redis credentials

var (
	cRedis *redis.Client
	ctx    = context.Background()
)

func init() {
	// Redis init
	cRedis = redis.NewClient(&redis.Options{
		Addr: os.Getenv("RD_ADDR"),
	})
	_, err := cRedis.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}
}

// Testing for processing of the Apache Kafka messages in the
// handlers.GetMsg() and handlers.ProcessMsg() functions.
func TestKafka(t *testing.T) {
	type args struct {
		data  models.FullName
		valid bool
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "Valid data was saved and enriched",
			args: args{
				valid: true,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Valid data with empty patronymic was saved and enriched",
			args: args{
				valid: true,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "Ivanov",
					Patronymic: "",
				},
			},
		},
		{
			test: "Valid data without patronymic was saved and enriched",
			args: args{
				valid: true,
				data: models.FullName{
					Name:    "Ivan",
					Surname: "Ivanov",
				},
			},
		},
		{
			test: "Empty name was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "",
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Data without name was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Less than 2 letters name was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "N",
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "More than 50 letters name was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name: `
						Nnnnnnnnnn
						Nnnnnnnnnn
						Nnnnnnnnnn
						Nnnnnnnnnn
						NnnnnnnnnnN
					`,
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Name with numbers was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "1Ivan",
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Name with symbols was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "!Ivan",
					Surname:    "Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Empty surname was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Data without surname was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "Ivan",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Less than 2 letters surname was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "S",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "More than 50 letters surname was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name: "Ivan",
					Surname: `
						Nnnnnnnnnn
						Nnnnnnnnnn
						Nnnnnnnnnn
						Nnnnnnnnnn
						NnnnnnnnnnN
					`,
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Surname with numbers was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "1Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
		{
			test: "Surname with symbols was rejected",
			args: args{
				valid: false,
				data: models.FullName{
					Name:       "Ivan",
					Surname:    "!Ivanov",
					Patronymic: "Ivanovich",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})

			// Run Kafka
			topics := kafka.Topics{
				{Name: os.Getenv("DATA_TEST"), Partitions: 1, Replication: 1},
				{Name: os.Getenv("FAIL_TEST"), Partitions: 1, Replication: 1},
			}
			kafka.Start(topics)
			dataTopic := topics[0]
			failTopic := topics[1]
			go handlers.GetMsg(dataTopic, failTopic)

			// Setup router
			r := router()
			_, err := cRedis.FlushAll(ctx).Result()
			assert.NoError(t, err)
			request, err := http.NewRequest(
				"GET",
				"http://127.0.0.1:8080/api/read",
				nil,
			)
			assert.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Produce testing data
			data := tt.args.data
			jsonData, err := json.Marshal(data)
			assert.NoError(t, err)
			testProducer := kafka.NewProd()
			dataTopic.Produce(jsonData, testProducer)

			// Estimation of values
			if tt.args.valid {
				var entry models.Entry
				i := 0
			VALIDATION:
				for {
					time.Sleep(1 * time.Second)
					query := db.C.First(&entry)
					switch {
					case query.Error != nil:
						i++
						continue
					case query.Error == nil:
						assert.NoError(t, query.Error)
						break VALIDATION
					case i > 10:
						assert.Error(t, errors.New("timeout request"))
						break VALIDATION
					}
				}
				assert.NotEqual(t, entry.Age, 0)
				assert.NotEqual(t, entry.Gender, "")
				assert.NotEqual(t, entry.Nationality, "")
			} else {
				failMsg := make(chan []byte)
				go failTopic.Consume(failMsg)
				msg := <-failMsg
				var failData models.FullName
				err = json.Unmarshal(msg, &failData)
				assert.Equal(t, data.Name, failData.Name)
				assert.Equal(t, data.Surname, failData.Surname)
				assert.Equal(t, data.Patronymic, failData.Patronymic)
				assert.NotEqual(t, failData.Error, "")
				assert.NoError(t, err)
			}
		})
	}
}

// Testing data processing in the handlers.Create() function.
func TestCreateAPI(t *testing.T) {
	type args struct {
		name        string
		surname     string
		patronymic  string
		age         uint8
		gender      string
		nationality string
		valid       bool
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "Valid data was saved",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       true,
			},
		},
		{
			test: "Valid data with empty patronymic was saved",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       true,
			},
		},
		{
			test: "Valid data without patronymic was saved",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       true,
			},
		},
		{
			test: "Empty name was rejected",
			args: args{
				name:        "",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Data without name was rejected",
			args: args{
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Less than 2 letters name was rejected",
			args: args{
				name:        "N",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "More than 50 letters name was rejected",
			args: args{
				name: `
					Nnnnnnnnnn
					Nnnnnnnnnn
					Nnnnnnnnnn
					Nnnnnnnnnn
					NnnnnnnnnnN
				`,
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Name with numbers was rejected",
			args: args{
				name:        "1Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Name with symbols was rejected",
			args: args{
				name:        "!Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Empty surname was rejected",
			args: args{
				name:        "Ivan",
				surname:     "",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Data without surname was rejected",
			args: args{
				name:        "Ivan",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Less than 2 letters surname was rejected",
			args: args{
				name:        "Ivan",
				surname:     "S",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "More than 50 letters surname was rejected",
			args: args{
				name: "Ivan",
				surname: `
					Nnnnnnnnnn
					Nnnnnnnnnn
					Nnnnnnnnnn
					Nnnnnnnnnn
					NnnnnnnnnnN
				`,
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Surname with numbers was rejected",
			args: args{
				name:        "Ivan",
				surname:     "1Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Surname with symbols was rejected",
			args: args{
				name:        "Ivan",
				surname:     "!Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Data without age was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Less than 1 age was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         0,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "More than 120 age was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         121,
				gender:      "male",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Empty gender was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Data without gender was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Non-existent gender was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "notexists",
				nationality: "RU",
				valid:       false,
			},
		},
		{
			test: "Empty nationality was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "",
				valid:       false,
			},
		},
		{
			test: "Data without nationality was rejected",
			args: args{
				name:       "Ivan",
				surname:    "Ivanov",
				patronymic: "Ivanovich",
				gender:     "male",
				age:        42,
				valid:      false,
			},
		},
		{
			test: "Less than 2 letters nationality was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "R",
				valid:       false,
			},
		},
		{
			test: "More than 2 letters nationality was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "RUS",
				valid:       false,
			},
		},
		{
			test: "Nationality with numbers was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "R7",
				valid:       false,
			},
		},
		{
			test: "Nationality with symbols was rejected",
			args: args{
				name:        "Ivan",
				surname:     "Ivanov",
				patronymic:  "Ivanovich",
				age:         42,
				gender:      "male",
				nationality: "R!",
				valid:       false,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})

			// Create testing data
			send := models.Entry{
				Name:        tt.args.name,
				Surname:     tt.args.surname,
				Patronymic:  tt.args.patronymic,
				Age:         tt.args.age,
				Gender:      tt.args.gender,
				Nationality: tt.args.nationality,
			}
			jsonData, err := json.Marshal(send)
			assert.NoError(t, err)

			// Setup router
			r := router()
			request, err := http.NewRequest(
				"POST",
				"http://127.0.0.1:8080/api/create",
				bytes.NewBuffer(jsonData),
			)
			assert.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			var entry models.Entry
			err = db.C.First(&entry).Error

			// Estimation of values
			if tt.args.valid {
				assert.Equal(t, 200, response.Code)
				assert.NoError(t, err)
			} else {
				assert.NotEqual(t, 200, response.Code)
				assert.Error(t, err)
			}
		})
	}
}

// Testing data processing in the handlers.Read() function.
func TestReadAPI(t *testing.T) {
	type args struct {
		valid   bool
		size    int
		page    int
		col     string
		data    string
		entries []models.Entry
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "The entries list with 3 records was return",
			args: args{
				valid: true,
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "The empty entries list was return",
			args: args{
				valid:   true,
				entries: []models.Entry{},
			},
		},
		{
			test: "Valid paginated data was return",
			args: args{
				valid: true,
				size:  1,
				page:  2,
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Valid filtrated data was return",
			args: args{
				valid: true,
				col:   "Name",
				data:  "Ivan",
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Filtration request without column was aborted",
			args: args{
				valid: false,
				col:   "",
				data:  "Ivan",
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Filtration request without data was aborted",
			args: args{
				valid: false,
				col:   "Name",
				data:  "",
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})

			// Create testing data
			db.C.Create(&tt.args.entries)
			_, err := cRedis.FlushAll(ctx).Result()
			assert.NoError(t, err)

			// Setup router
			r := router()
			url := ""
			var pagination []string
			intSize := 10
			intPage := 1
			if tt.args.size != 0 {
				pagination = append(
					pagination,
					fmt.Sprintf("size=%v", tt.args.size),
				)
				intSize = tt.args.size
			}
			if tt.args.page != 0 {
				pagination = append(
					pagination,
					fmt.Sprintf("page=%v", tt.args.page),
				)
				intPage = tt.args.page
			}
			if tt.args.col != "" {
				pagination = append(pagination, "col="+tt.args.col)
			}
			if tt.args.data != "" {
				pagination = append(pagination, "data="+tt.args.data)
			}
			if len(pagination) == 0 {
				url = "http://127.0.0.1:8080/api/read"
			} else {
				params := strings.Join(pagination, "&")
				url = "http://127.0.0.1:8080/api/read?" + params
			}
			request, err := http.NewRequest(
				"GET",
				url,
				nil,
			)
			assert.NoError(t, err)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			offset := (intPage - 1) * intSize
			var entries []models.Entry
			switch {
			case tt.args.col != "" && tt.args.data != "":
				err = db.C.Model(&models.Entry{}).
					Limit(intSize).
					Offset(offset).
					Where(tt.args.col+" LIKE ?", "%"+tt.args.data+"%").
					Find(&entries).
					Error
			default:
				err = db.C.Model(&models.Entry{}).
					Limit(intSize).
					Offset(offset).
					Find(&entries).
					Error
			}
			assert.NoError(t, err)
			entriesJSON, err := json.Marshal(gin.H{"entries": entries})
			assert.NoError(t, err)

			// Estimation of values
			if tt.args.valid {
				assert.Equal(t, 200, response.Code)
				assert.JSONEq(
					t,
					string(entriesJSON),
					strings.TrimSpace(response.Body.String()),
				)
			} else {
				assert.Equal(t, 400, response.Code)
				assert.NotEqual(
					t,
					string(entriesJSON),
					strings.TrimSpace(response.Body.String()),
				)
			}
		})
	}
}

// Testing data processing in the handlers.Update() function.
func TestUpdateAPI(t *testing.T) {
	// Setup test database
	gin.SetMode(gin.TestMode)
	db.Connect()
	db.C.AutoMigrate(&models.Entry{})
	defer db.C.Migrator().DropTable(&models.Entry{})
	data := models.Entry{
		Name:        "Ivan",
		Surname:     "Ivanov",
		Patronymic:  "Ivanovich",
		Age:         42,
		Gender:      "male",
		Nationality: "RU",
	}
	err := db.C.Create(&data).Error
	assert.NoError(t, err)

	// Create testing data
	send := models.Entry{
		ID:          1,
		Name:        "Ivan",
		Surname:     "Smirnov",
		Patronymic:  "Ivanovich",
		Age:         42,
		Gender:      "male",
		Nationality: "RU",
	}
	jsonData, err := json.Marshal(send)
	assert.NoError(t, err)

	// Setup router
	r := router()
	request, err := http.NewRequest(
		"PATCH",
		"http://127.0.0.1:8080/api/update",
		bytes.NewBuffer(jsonData),
	)
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	// Get database values
	var entry models.Entry
	err = db.C.Where("name = ?", data.Name).First(&entry).Error

	// Estimation of values
	assert.Equal(t, 200, response.Code)
	assert.NoError(t, err)
	assert.Equal(t, send.Surname, entry.Surname)
}

// Testing data processing in the handlers.Delete() function.
func TestDeleteAPI(t *testing.T) {
	// Setup test database
	gin.SetMode(gin.TestMode)
	db.Connect()
	db.C.AutoMigrate(&models.Entry{})
	defer db.C.Migrator().DropTable(&models.Entry{})
	data := models.Entry{
		Name:        "Ivan",
		Surname:     "Ivanov",
		Patronymic:  "Ivanovich",
		Age:         42,
		Gender:      "male",
		Nationality: "RU",
	}
	err := db.C.Create(&data).Error
	assert.NoError(t, err)

	// Create testing data
	send := models.Entry{
		ID: 1,
	}
	jsonData, err := json.Marshal(send)
	assert.NoError(t, err)

	// Setup router
	r := router()
	request, err := http.NewRequest(
		"DELETE",
		"http://127.0.0.1:8080/api/delete",
		bytes.NewBuffer(jsonData),
	)
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	// Get database values
	var entries []models.Entry
	err = db.C.Find(&entries).Error
	assert.NoError(t, err)
	entriesJSON, err := json.Marshal(gin.H{"entries": entries})
	assert.NoError(t, err)

	// Estimation of values
	assert.Equal(t, 200, response.Code)
	assert.Equal(t, string(entriesJSON), "{\"entries\":[]}")
}

// Testing of data creation in the handlers.GraphQL() function.
func TestCreateGraphQL(t *testing.T) {
	tests := []struct {
		test  string
		valid bool
		query string
	}{
		{
			test:  "Valid data was saved",
			valid: true,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Valid data with empty patronymic was saved",
			valid: true,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Valid data without patronymic was saved",
			valid: true,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Empty name was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					CreatedAt 
					UpdatedAt
					DeletedAt  
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Empty name was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					CreatedAt 
					UpdatedAt
					DeletedAt  
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Data without name was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Less than 2 letters name was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "N",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "More than 50 letters name was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name: "NnnnnnnnnnNnnnnnnnnnNnnnnnnnnnNnnnnnnnnnNnnnnnnnnnN",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Name with numbers was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "1Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Name with symbols was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "!Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Name with incorrect type was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        0,
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Empty surname was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Data without surname was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Less than 2 letters surname was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "S",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "More than 50 letters surname was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "NnnnnnnnnnNnnnnnnnnnNnnnnnnnnnNnnnnnnnnnNnnnnnnnnnN",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Surname with numbers was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "1Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Surname with symbols was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "!Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Surname with incorrect type was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     0,
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Data without age was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Less than 1 age was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         0,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "More than 120 age was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         121,
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Age with incorrect type was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         "42",
					gender:      "male",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Empty gender was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Data without gender was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Non-existent gender was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "nonexist",
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Gender with incorrect type was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      0,
					nationality: "RU",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Empty nationality was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Data without nationality was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Less than 2 letters nationality was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "R",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "More than 2 letters nationality was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "RUS",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Nationality with numbers was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "R7",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Nationality with symbols was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: "R!",
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
		{
			test:  "Nationality with incorrect type was rejected",
			valid: false,
			query: `mutation {
				created_entry(
					name:        "Ivan",
					surname:     "Ivanov",
					patronymic:  "Ivanovich",
					age:         42,
					gender:      "male",
					nationality: 42,
				) {
					ID
					Name
					Surname
					Patronymic
					Age
					Gender
					Nationality
				}
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})

			// Create testing data
			send := map[string]string{
				"query": tt.query,
			}
			jsonData, err := json.Marshal(send)
			assert.NoError(t, err)

			// Setup router
			r := router()
			request, err := http.NewRequest(
				"POST",
				"http://127.0.0.1:8080/graphql",
				bytes.NewBuffer(jsonData),
			)
			assert.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			var entry models.Entry
			query := db.C.First(&entry)
			value := models.GraphQL{
				ID:          entry.ID,
				Name:        entry.Name,
				Surname:     entry.Surname,
				Patronymic:  entry.Patronymic,
				Age:         entry.Age,
				Gender:      entry.Gender,
				Nationality: entry.Nationality,
			}
			entriesJSON, err := json.Marshal(
				gin.H{"data": gin.H{"created_entry": value}},
			)
			assert.NoError(t, err)

			// Estimation of values
			if tt.valid {
				assert.Equal(t, 200, response.Code)
				assert.NoError(t, query.Error)
				assert.JSONEq(
					t,
					string(entriesJSON),
					response.Body.String(),
				)
			} else {
				assert.NotEqual(t, 200, response.Code)
				assert.Error(t, query.Error)
			}
		})
	}
}

// Testing of data obtaining in the handlers.GraphQL() function.
func TestReadGraphQL(t *testing.T) {
	type args struct {
		valid bool
		size  int
		page  int
		col   string
		data  string
		query string
		slice []models.Entry
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "The entries list with 3 records was return",
			args: args{
				valid: true,
				query: `query {
					entries {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				slice: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Valid paginated data was return",
			args: args{
				valid: true,
				size:  1,
				page:  2,
				query: `query {
					entries (
						size: 1,
						page: 2,
					) {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				slice: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Valid filtrated data was return",
			args: args{
				valid: true,
				col:   "Name",
				data:  "Ivan",
				query: `query {
					entries (
						col: "Name",
						data: "Ivan",
					) {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				slice: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Filtration request without column was aborted",
			args: args{
				valid: false,
				data:  "Ivan",
				query: `query {
					entries (
						data: "Ivan",
					) {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				slice: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Filtration request without data was aborted",
			args: args{
				valid: false,
				col:   "Name",
				query: `query {
					entries (
						col: "Name",
					) {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				slice: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
					{
						Name:        "Anna",
						Surname:     "Ivanova",
						Patronymic:  "Ivanovna",
						Age:         42,
						Gender:      "female",
						Nationality: "RU",
					},
					{
						Name:        "Ivan",
						Surname:     "Ushakov",
						Patronymic:  "Vasilevich",
						Age:         30,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})
			data := tt.args.slice
			db.C.Create(&data)
			_, err := cRedis.FlushAll(ctx).Result()
			assert.NoError(t, err)

			// Create testing data
			send := map[string]string{
				"query": tt.args.query,
			}
			jsonData, err := json.Marshal(send)
			assert.NoError(t, err)

			// Setup router
			r := router()
			request, err := http.NewRequest(
				"POST",
				"http://127.0.0.1:8080/graphql",
				bytes.NewBuffer(jsonData),
			)
			assert.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			intSize := 10
			intPage := 1
			if tt.args.size != 0 {
				intSize = tt.args.size
			}
			if tt.args.page != 0 {
				intPage = tt.args.page
			}
			offset := (intPage - 1) * intSize
			var query *gorm.DB
			var entries []models.Entry
			switch {
			case tt.args.col != "" && tt.args.data != "":
				query = db.C.Model(&models.Entry{}).
					Limit(intSize).
					Offset(offset).
					Where(tt.args.col+" LIKE ?", "%"+tt.args.data+"%").
					Find(&entries)
			default:
				query = db.C.Model(&models.Entry{}).
					Limit(intSize).
					Offset(offset).
					Find(&entries)
			}
			assert.NoError(t, query.Error)
			var values []models.GraphQL
			for _, entry := range entries {
				value := models.GraphQL{
					ID:          entry.ID,
					Name:        entry.Name,
					Surname:     entry.Surname,
					Patronymic:  entry.Patronymic,
					Age:         entry.Age,
					Gender:      entry.Gender,
					Nationality: entry.Nationality,
				}
				values = append(values, value)
			}
			entriesJSON, err := json.Marshal(
				gin.H{"data": gin.H{"entries": values}},
			)
			assert.NoError(t, err)

			// Estimation of values
			if tt.args.valid {
				assert.Equal(t, 200, response.Code)
				assert.NoError(t, query.Error)
				assert.JSONEq(
					t,
					string(entriesJSON),
					response.Body.String(),
				)
			} else {
				assert.NotEqual(t, 200, response.Code)
				assert.NotEqual(
					t,
					string(entriesJSON),
					response.Body.String(),
				)
			}
		})
	}
}

// Testing of data updating in the handlers.GraphQL() function.
func TestUpdateGraphQL(t *testing.T) {
	// Setup test database
	gin.SetMode(gin.TestMode)
	db.Connect()
	db.C.AutoMigrate(&models.Entry{})
	defer db.C.Migrator().DropTable(&models.Entry{})
	data := models.Entry{
		Name:        "Ivan",
		Surname:     "Ivanov",
		Patronymic:  "Ivanovich",
		Age:         42,
		Gender:      "male",
		Nationality: "RU",
	}
	err := db.C.Create(&data).Error
	assert.NoError(t, err)

	// Create testing data
	send := map[string]string{
		"query": `mutation {
			updated_entry(
				id: 1, 
				name: "Ivan",
				surname: "Smirnov",
				patronymic: "Ivanovich",
				age: 42
				gender: "male",
				nationality: "RU",
			) {
				ID
				Name
				Surname
				Patronymic
				Age
				Gender
				Nationality
			}
		}`,
	}
	jsonData, err := json.Marshal(send)
	assert.NoError(t, err)

	// Setup router
	r := router()
	request, err := http.NewRequest(
		"POST",
		"http://127.0.0.1:8080/graphql",
		bytes.NewBuffer(jsonData),
	)
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	// Get database values
	var entry models.Entry
	err = db.C.Where("name = ?", data.Name).First(&entry).Error

	// Estimation of values
	assert.Equal(t, 200, response.Code)
	assert.NoError(t, err)
	assert.Equal(t, "Smirnov", entry.Surname)
}

// Testing of data deleting in the handlers.GraphQL() function.
func TestDeleteGraphQL(t *testing.T) {
	// Setup test database
	gin.SetMode(gin.TestMode)
	db.Connect()
	db.C.AutoMigrate(&models.Entry{})
	defer db.C.Migrator().DropTable(&models.Entry{})
	data := models.Entry{
		Name:        "Ivan",
		Surname:     "Ivanov",
		Patronymic:  "Ivanovich",
		Age:         42,
		Gender:      "male",
		Nationality: "RU",
	}
	err := db.C.Create(&data).Error
	assert.NoError(t, err)

	// Create testing data
	send := map[string]string{
		"query": `mutation {
			deleted_entry(
				id: 1,
			) {
				ID
				Name
				Surname
				Patronymic
				Age
				Gender
				Nationality
			}
		}`,
	}
	jsonData, err := json.Marshal(send)
	assert.NoError(t, err)

	// Setup router
	r := router()
	request, err := http.NewRequest(
		"POST",
		"http://127.0.0.1:8080/graphql",
		bytes.NewBuffer(jsonData),
	)
	assert.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	// Get database values
	var entries []models.Entry
	err = db.C.Find(&entries).Error
	assert.NoError(t, err)
	entriesJSON, err := json.Marshal(gin.H{"entries": entries})
	assert.NoError(t, err)

	// Estimation of values
	assert.Equal(t, 200, response.Code)
	assert.Equal(t, string(entriesJSON), "{\"entries\":[]}")
}

// Testing of data caching in the handlers.Read() function.
func TestCacheAPI(t *testing.T) {
	type args struct {
		entries []models.Entry
		cached  bool
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "Data was sent for caching",
			args: args{
				cached: true,
				entries: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Cached data was return",
			args: args{
				cached:  false,
				entries: []models.Entry{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Empty cache for first run
			if tt.args.cached {
				_, err := cRedis.FlushAll(ctx).Result()
				assert.NoError(t, err)
			}

			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})

			// Create testing data
			db.C.Create(&tt.args.entries)

			// Setup router
			r := router()
			request, err := http.NewRequest(
				"GET",
				"http://127.0.0.1:8080/api/read",
				nil,
			)
			assert.NoError(t, err)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			var entries []models.Entry
			err = db.C.Find(&entries).Error
			assert.NoError(t, err)
			entriesJSON, err := json.Marshal(gin.H{"entries": entries})
			assert.NoError(t, err)

			// Estimation of values
			if tt.args.cached {
				assert.Equal(t, 200, response.Code)
				assert.JSONEq(
					t,
					string(entriesJSON),
					strings.TrimSpace(response.Body.String()),
				)
			} else {
				assert.Equal(t, 200, response.Code)
				assert.NotEqual(
					t,
					string(entriesJSON),
					strings.TrimSpace(response.Body.String()),
				)
			}
		})
	}
}

// Testing of data caching in the handlers.GraphQL() function.
func TestCacheGraphQL(t *testing.T) {
	type args struct {
		cached bool
		query  string
		data   []models.Entry
	}
	tests := []struct {
		test string
		args args
	}{
		{
			test: "Data was sent for caching",
			args: args{
				cached: true,
				query: `query {
					entries {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				data: []models.Entry{
					{
						Name:        "Ivan",
						Surname:     "Ivanov",
						Patronymic:  "Ivanovich",
						Age:         42,
						Gender:      "male",
						Nationality: "RU",
					},
				},
			},
		},
		{
			test: "Cached data was return",
			args: args{
				cached: false,
				query: `query {
					entries {
						ID
						Name
						Surname
						Patronymic
						Age
						Gender
						Nationality
					}
				}`,
				data: []models.Entry{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.test, func(t *testing.T) {
			// Empty cache for first run
			if tt.args.cached {
				_, err := cRedis.FlushAll(ctx).Result()
				assert.NoError(t, err)
			}

			// Setup test database
			gin.SetMode(gin.TestMode)
			db.Connect()
			db.C.AutoMigrate(&models.Entry{})
			defer db.C.Migrator().DropTable(&models.Entry{})
			data := tt.args.data
			db.C.Create(&data)

			// Create testing data
			send := map[string]string{
				"query": tt.args.query,
			}
			jsonData, err := json.Marshal(send)
			assert.NoError(t, err)

			// Setup router
			r := router()
			request, err := http.NewRequest(
				"POST",
				"http://127.0.0.1:8080/graphql",
				bytes.NewBuffer(jsonData),
			)
			assert.NoError(t, err)
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)

			// Get database values
			var entries []models.Entry
			query := db.C.Find(&entries)
			var values []models.GraphQL
			for _, entry := range entries {
				value := models.GraphQL{
					ID:          entry.ID,
					Name:        entry.Name,
					Surname:     entry.Surname,
					Patronymic:  entry.Patronymic,
					Age:         entry.Age,
					Gender:      entry.Gender,
					Nationality: entry.Nationality,
				}
				values = append(values, value)
			}
			entriesJSON, err := json.Marshal(
				gin.H{"data": gin.H{"entries": values}},
			)
			assert.NoError(t, err)

			// Estimation of values
			if tt.args.cached {
				assert.Equal(t, 200, response.Code)
				assert.NoError(t, query.Error)
				assert.JSONEq(
					t,
					string(entriesJSON),
					response.Body.String(),
				)
			} else {
				assert.Equal(t, 200, response.Code)
				assert.NotEqual(
					t,
					string(entriesJSON),
					strings.TrimSpace(response.Body.String()),
				)
			}
		})
	}
}
