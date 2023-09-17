package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	db "people/database"
	"people/kafka"
	"people/logging"
	"people/models"
	"strconv"

	"github.com/IBM/sarama"
	"github.com/gin-gonic/gin"
	"github.com/graphql-go/graphql"
	_ "github.com/joho/godotenv/autoload"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

var (
	cRedis       *redis.Client
	dataTopic    kafka.Topic
	failTopic    kafka.Topic
	failProducer sarama.AsyncProducer
	dataCh       = make(chan []byte)
	ctx          = context.Background()
	log          = logging.Config
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

// The function triggers the consumer and producer of messages.
func GetMsg(data kafka.Topic, fail kafka.Topic) {
	dataTopic = data
	failTopic = fail
	failProducer = kafka.NewProd()
	go dataTopic.Consume(dataCh)
	for {
		go ProcessMsg(<-dataCh)
	}
}

// The function processes, checks, enriches and saves correct incoming
// messages to the database. Incorrect messages are enriched with the
// cause of the error and sent to a separate topic.
func ProcessMsg(msg []byte) {
	f := logging.F()
	var dataMsg models.FullName
	err := json.Unmarshal(msg, &dataMsg)
	if err != nil {
		log.Error(f+"JSON deserializing failed: ", err)
		failTopic.Produce(msg, failProducer)
		return
	}
	log.WithFields(logrus.Fields{
		"Name":       dataMsg.Name,
		"Surname":    dataMsg.Surname,
		"Patronymic": dataMsg.Patronymic,
	}).Debug(f + "dataMsg")
	result := dataMsg.IsValid()
	if result != "" {
		log.Debug(f+"invalid message: ", result)
		dataMsg.Error = result
		jsonData, err := json.Marshal(dataMsg)
		if err != nil {
			log.Error(f+"serializing to JSON failed: ", err)
			failTopic.Produce(msg, failProducer)
			return
		}
		failTopic.Produce(jsonData, failProducer)
		return
	}
	entry := models.Entry{
		Name:       dataMsg.Name,
		Surname:    dataMsg.Surname,
		Patronymic: dataMsg.Patronymic,
	}
	err = entry.Enrich(entry.Name)
	if err != nil {
		log.Error(f+"failed to enrich data from API: ", err)
		dataMsg.Error = fmt.Sprintf("Failed to enrich data from API: %v", err)
		jsonData, err := json.Marshal(dataMsg)
		if err != nil {
			log.Error(f+"serializing to JSON failed: ", err)
			failTopic.Produce(msg, failProducer)
			return
		}
		failTopic.Produce(jsonData, failProducer)
		return
	}
	log.WithFields(logrus.Fields{
		"ID":          entry.ID,
		"Name":        entry.Name,
		"Surname":     entry.Surname,
		"Patronymic":  entry.Patronymic,
		"Age":         entry.Age,
		"Gender":      entry.Gender,
		"Nationality": entry.Nationality,
	}).Debug(f + "entry")
	err = db.C.Create(&entry).Error
	if err != nil {
		log.Error(f+"failed to create entry: ", err)
		dataMsg.Error = fmt.Sprintf("Failed to create entry: %v", err)
		jsonData, err := json.Marshal(dataMsg)
		if err != nil {
			log.Error(f+"serializing to JSON failed: ", err)
			failTopic.Produce(msg, failProducer)
			return
		}
		failTopic.Produce(jsonData, failProducer)
		return
	}
	status, err := cRedis.FlushAll(ctx).Result()
	if err != nil {
		log.Error(f+"FLUSHALL failed: ", err)
	} else {
		log.Debug(f+"FLUSHALL success: ", status)
	}
}

// This API handler checks the input data, saves the record into the
// database and dumps the Redis cache keys. Return a JSON success
// message or an error with its cause.
func Create(c *gin.Context) {
	f := logging.F()
	var newEntry models.Entry
	if err := c.ShouldBind(&newEntry); err != nil {
		log.Debug(f+"parsing failed: ", err)
		c.JSON(400, gin.H{"error": "Invalid API query"})
		return
	}
	log.WithFields(logrus.Fields{
		"Name":        newEntry.Name,
		"Surname":     newEntry.Surname,
		"Patronymic":  newEntry.Patronymic,
		"Age":         newEntry.Age,
		"Gender":      newEntry.Gender,
		"Nationality": newEntry.Nationality,
	}).Debug(f + "newEntry")
	err := newEntry.IsValid()
	if err != nil {
		c.JSON(422, gin.H{"error": fmt.Sprintf("Filling errors: %v", err)})
		return
	}
	err = db.C.Create(&newEntry).Error
	if err != nil {
		log.Error(f+"failed to create entry: ", err)
		c.JSON(500, gin.H{"error": "Failed to create entry"})
		return
	}
	status, err := cRedis.FlushAll(ctx).Result()
	if err != nil {
		log.Error(f+"FLUSHALL failed: ", err)
	} else {
		log.Debug(f+"FLUSHALL success: ", status)
	}
	c.JSON(200, gin.H{"message": "Success"})
}

// This API handler reads filtering parameters, creates a caching key
// to obtain data from Redis, otherwise it reads data from the database
// with their conservation in cache. Return a JSON message with data or
// an error with its cause.
func Read(c *gin.Context) {
	f := logging.F()
	pageSize := c.DefaultQuery("size", "10")
	pageNum := c.DefaultQuery("page", "1")
	filterCol := c.Query("col")
	filterData := c.Query("data")
	log.WithFields(logrus.Fields{
		"Size":   pageSize,
		"Num":    pageNum,
		"Column": filterCol,
		"Data":   filterData,
	}).Debug(f + "GET filters")
	switch {
	case filterCol != "" && filterData == "":
		fallthrough
	case filterCol == "" && filterData != "":
		c.JSON(400, gin.H{"error": `Fill in both "col" and "data"`})
		return
	}
	intSize, err := strconv.Atoi(pageSize)
	if err != nil {
		log.Debug(f+"invalid page size: ", err)
		c.JSON(400, gin.H{"error": "Invalid size parameter"})
		return
	}
	intPage, err := strconv.Atoi(pageNum)
	if err != nil {
		log.Debug(f+"invalid page number: ", err)
		c.JSON(400, gin.H{"error": "Invalid page parameter"})
		return
	}
	offset := (intPage - 1) * intSize
	var entries []models.Entry
	cacheKey := fmt.Sprintf(
		"entries:%v:%v:%s:%s", intSize, intPage, filterCol, filterData,
	)
	log.WithFields(logrus.Fields{
		"Key": cacheKey,
	}).Debug(f + "Redis cache key")
	cacheResult, err := cRedis.Get(ctx, cacheKey).Result()
	if err == nil {
		err := json.Unmarshal([]byte(cacheResult), &entries)
		if err != nil {
			log.Error(f+"JSON deserializing failed: ", err)
		}
		log.Info(f + "data from CACHE")
		c.JSON(200, gin.H{"entries": entries})
		return
	}
	log.Debug(f+"cache error: ", err)
	switch {
	case filterCol != "" && filterData != "":
		err = db.C.Model(&models.Entry{}).
			Limit(intSize).
			Offset(offset).
			Where(filterCol+" LIKE ?", "%"+filterData+"%").
			Find(&entries).
			Error
	default:
		err = db.C.Model(&models.Entry{}).
			Limit(intSize).
			Offset(offset).
			Find(&entries).
			Error
	}
	if err != nil {
		log.Error(f+"request to the database failed: ", err)
		c.JSON(500, gin.H{"error": "Request failed"})
		return
	}
	log.Info(f + "data from DATABASE")
	jsonData, err := json.Marshal(entries)
	if err != nil {
		log.Error(f+"serializing to JSON failed: ", err)
	}
	cRedis.Set(ctx, cacheKey, jsonData, 0)
	c.JSON(200, gin.H{"entries": entries})
}

// This API handler checks the input data, updates the record into the
// database and dumps the Redis cache keys. Return a JSON success
// message or an error with its cause.
func Update(c *gin.Context) {
	f := logging.F()
	var updEntry models.Entry
	if err := c.ShouldBind(&updEntry); err != nil {
		log.Debug(f+"parsing failed: ", err)
		c.JSON(400, gin.H{"error": "Invalid API query"})
		return
	}
	log.WithFields(logrus.Fields{
		"ID":          updEntry.ID,
		"Name":        updEntry.Name,
		"Surname":     updEntry.Surname,
		"Patronymic":  updEntry.Patronymic,
		"Age":         updEntry.Age,
		"Gender":      updEntry.Gender,
		"Nationality": updEntry.Nationality,
	}).Debug(f + "updEntry")
	err := updEntry.IsValid()
	if err != nil {
		c.JSON(422, gin.H{"error": fmt.Sprintf("Filling errors: %v", err)})
		return
	}
	err = db.C.Model(&models.Entry{}).
		Where("id = ?", updEntry.ID).
		Updates(map[string]interface{}{
			"name":        updEntry.Name,
			"surname":     updEntry.Surname,
			"patronymic":  updEntry.Patronymic,
			"age":         updEntry.Age,
			"gender":      updEntry.Gender,
			"nationality": updEntry.Nationality,
		}).
		Error
	if err != nil {
		c.JSON(
			404,
			gin.H{"message": fmt.Sprintf(
				`Entry "%v" does not exist`,
				updEntry.ID,
			)},
		)
		return
	}
	status, err := cRedis.FlushAll(ctx).Result()
	if err != nil {
		log.Error(f+"FLUSHALL failed: ", err)
	} else {
		log.Debug(f+"FLUSHALL success: ", status)
	}
	c.JSON(200, gin.H{"message": "Success"})
}

// This API handler checks the input ID, deletes the record from the
// database and dumps the Redis cache keys. Return a JSON success
// message or an error with its cause.
func Delete(c *gin.Context) {
	f := logging.F()
	var delEntry models.Entry
	if err := c.ShouldBind(&delEntry); err != nil {
		log.Debug(f+"parsing failed: ", err)
		c.JSON(400, gin.H{"error": "Invalid API query"})
		return
	}
	log.WithFields(logrus.Fields{
		"ID": delEntry.ID,
	}).Debug(f + "delEntry")
	var entry models.Entry
	err := db.C.First(&entry, "id = ?", delEntry.ID).Error
	if err != nil {
		c.JSON(
			404,
			gin.H{"message": fmt.Sprintf(
				`Entry "%v" does not exist`,
				delEntry.ID,
			)},
		)
		return
	}
	err = db.C.Unscoped().Delete(&entry).Error
	if err != nil {
		log.Error(f+"failed to delete entry: ", err)
		c.JSON(500, gin.H{"error": "Failed to delete entry"})
		return
	}
	status, err := cRedis.FlushAll(ctx).Result()
	if err != nil {
		log.Error(f+"FLUSHALL failed: ", err)
	} else {
		log.Debug(f+"FLUSHALL success: ", status)
	}
	c.JSON(200, gin.H{"message": "Success"})
}

// The main GraphQL handler. Reads the query data and performs
// operations in accordance with the scheme. Return a JSON message with
// data or an error with its cause.
func GraphQL(c *gin.Context) {
	f := logging.F()
	var req struct {
		Query string `json:"query"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Debug(f+"parsing failed: ", err)
		c.JSON(400, gin.H{"error": "Invalid GraphQL query"})
		return
	}
	result := graphql.Do(graphql.Params{
		Schema:        schema,
		RequestString: req.Query,
	})
	if len(result.Errors) > 0 {
		c.JSON(400, gin.H{"errors": result.Errors})
		return
	}
	c.JSON(200, gin.H{"data": result.Data})
}

// The processing scheme of root queries.
var schema, _ = graphql.NewSchema(graphql.SchemaConfig{
	Query:    rootQuery,
	Mutation: rootMutation,
})

// GraphQL data fields for the Entry model.
var entryType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Entry",
	Fields: graphql.Fields{
		"ID":          &graphql.Field{Type: graphql.Int},
		"Name":        &graphql.Field{Type: graphql.String},
		"Surname":     &graphql.Field{Type: graphql.String},
		"Patronymic":  &graphql.Field{Type: graphql.String},
		"Age":         &graphql.Field{Type: graphql.Int},
		"Gender":      &graphql.Field{Type: graphql.String},
		"Nationality": &graphql.Field{Type: graphql.String},
	},
})

// The parameters of the root query for reading data and its handler.
var rootQuery = graphql.NewObject(graphql.ObjectConfig{
	Name: "RootQuery",
	Fields: graphql.Fields{
		"entries": &graphql.Field{
			Type: graphql.NewList(entryType),
			Args: graphql.FieldConfigArgument{
				"size": &graphql.ArgumentConfig{
					Type:         graphql.Int,
					DefaultValue: 10,
				},
				"page": &graphql.ArgumentConfig{
					Type:         graphql.Int,
					DefaultValue: 1,
				},
				"col": &graphql.ArgumentConfig{
					Type:         graphql.String,
					DefaultValue: "",
				},
				"data": &graphql.ArgumentConfig{
					Type:         graphql.String,
					DefaultValue: "",
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				f := logging.F()
				intSize, _ := p.Args["size"].(int)
				intPage, _ := p.Args["page"].(int)
				filterCol, _ := p.Args["col"].(string)
				filterData, _ := p.Args["data"].(string)
				switch {
				case filterCol != "" && filterData == "":
					fallthrough
				case filterCol == "" && filterData != "":
					return nil, errors.New(`fill in both "col" and "data"`)
				}
				offset := (intPage - 1) * intSize
				var entries []models.Entry
				cacheKey := fmt.Sprintf(
					"entries:%v:%v:%s:%s",
					intSize,
					intPage,
					filterCol,
					filterData,
				)
				log.WithFields(logrus.Fields{
					"Key": cacheKey,
				}).Debug(f + "Redis cache key")
				cacheResult, err := cRedis.Get(ctx, cacheKey).Result()
				if err == nil {
					err := json.Unmarshal([]byte(cacheResult), &entries)
					if err != nil {
						log.Error(f+"JSON deserializing failed: ", err)
					}
					log.Info(f + "data from CACHE")
					return entries, nil
				}
				switch {
				case filterCol != "" && filterData != "":
					err = db.C.Model(&models.Entry{}).
						Limit(intSize).
						Offset(offset).
						Where(filterCol+" LIKE ?", "%"+filterData+"%").
						Find(&entries).
						Error
				default:
					err = db.C.Model(&models.Entry{}).
						Limit(intSize).
						Offset(offset).
						Find(&entries).
						Error
				}
				if err != nil {
					log.Error(
						f+"request to the database failed: ",
						err,
					)
					return nil, err
				}
				log.Info(f + "data from DATABASE")
				jsonData, err := json.Marshal(entries)
				if err != nil {
					log.Error(f+"serializing to JSON failed: ", err)
				}
				cRedis.Set(ctx, cacheKey, jsonData, 0)
				return entries, nil
			},
		},
	},
})

// The parameters of the root query for data changes and its handler.
var rootMutation = graphql.NewObject(graphql.ObjectConfig{
	Name: "RootMutation",
	Fields: graphql.Fields{
		"created_entry": &graphql.Field{
			Type: entryType,
			Args: graphql.FieldConfigArgument{
				"name": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"surname": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"patronymic": &graphql.ArgumentConfig{
					Type: graphql.String,
				},
				"age": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.Int),
				},
				"gender": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"nationality": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				f := logging.F()
				name, _ := p.Args["name"].(string)
				surname, _ := p.Args["surname"].(string)
				patronymic, _ := p.Args["patronymic"].(string)
				age, _ := p.Args["age"].(int)
				gender, _ := p.Args["gender"].(string)
				nationality, _ := p.Args["nationality"].(string)
				newEntry := models.Entry{
					Name:        name,
					Surname:     surname,
					Patronymic:  patronymic,
					Age:         uint8(age),
					Gender:      gender,
					Nationality: nationality,
				}
				log.WithFields(logrus.Fields{
					"Name":        newEntry.Name,
					"Surname":     newEntry.Surname,
					"Patronymic":  newEntry.Patronymic,
					"Age":         newEntry.Age,
					"Gender":      newEntry.Gender,
					"Nationality": newEntry.Nationality,
				}).Debug(f + "newEntry")
				err := newEntry.IsValid()
				if err != nil {
					return nil, err
				}
				err = db.C.Create(&newEntry).Error
				if err != nil {
					log.Error(f+"failed to create entry: ", err)
					return nil, err
				}
				status, err := cRedis.FlushAll(ctx).Result()
				if err != nil {
					log.Error(f+"FLUSHALL failed: ", err)
				} else {
					log.Debug(f+"FLUSHALL success: ", status)
				}
				return newEntry, nil
			},
		},
		"updated_entry": &graphql.Field{
			Type: entryType,
			Args: graphql.FieldConfigArgument{
				"id": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.Int),
				},
				"name": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"surname": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"patronymic": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"age": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.Int),
				},
				"gender": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
				"nationality": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.String),
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				f := logging.F()
				id, _ := p.Args["id"].(int)
				name, _ := p.Args["name"].(string)
				surname, _ := p.Args["surname"].(string)
				patronymic, _ := p.Args["patronymic"].(string)
				age, _ := p.Args["age"].(int)
				gender, _ := p.Args["gender"].(string)
				nationality, _ := p.Args["nationality"].(string)
				updEntry := models.Entry{
					ID:          uint(id),
					Name:        name,
					Surname:     surname,
					Patronymic:  patronymic,
					Age:         uint8(age),
					Gender:      gender,
					Nationality: nationality,
				}
				log.WithFields(logrus.Fields{
					"ID":          updEntry.ID,
					"Name":        updEntry.Name,
					"Surname":     updEntry.Surname,
					"Patronymic":  updEntry.Patronymic,
					"Age":         updEntry.Age,
					"Gender":      updEntry.Gender,
					"Nationality": updEntry.Nationality,
				}).Debug(f + "updEntry")
				err := updEntry.IsValid()
				if err != nil {
					return nil, err
				}
				err = db.C.Model(&models.Entry{}).
					Where("id = ?", updEntry.ID).
					Updates(map[string]interface{}{
						"name":        updEntry.Name,
						"surname":     updEntry.Surname,
						"patronymic":  updEntry.Patronymic,
						"age":         updEntry.Age,
						"gender":      updEntry.Gender,
						"nationality": updEntry.Nationality,
					}).
					Error
				if err != nil {
					return nil, err
				}
				status, err := cRedis.FlushAll(ctx).Result()
				if err != nil {
					log.Error(f+"FLUSHALL failed: ", err)
				} else {
					log.Debug(f+"FLUSHALL success: ", status)
				}
				return updEntry, nil
			},
		},
		"deleted_entry": &graphql.Field{
			Type: entryType,
			Args: graphql.FieldConfigArgument{
				"id": &graphql.ArgumentConfig{
					Type: graphql.NewNonNull(graphql.Int),
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				f := logging.F()
				id, _ := p.Args["id"].(int)
				delEntry := models.Entry{
					ID: uint(id),
				}
				log.WithFields(logrus.Fields{
					"ID": delEntry.ID,
				}).Debug(f + "delEntry")
				err := db.C.First(&delEntry, "id = ?", delEntry.ID).Error
				if err != nil {
					return nil, err
				}
				err = db.C.Unscoped().Delete(&delEntry).Error
				if err != nil {
					log.Error(f+"failed to delete entry: ", err)
					return nil, err
				}
				status, err := cRedis.FlushAll(ctx).Result()
				if err != nil {
					log.Error(f+"FLUSHALL failed: ", err)
				} else {
					log.Debug(f+"FLUSHALL success: ", status)
				}
				return delEntry, nil
			},
		},
	},
})

/* func createQuery(f string, newEntry models.Entry) (int, string, error) {
	err := newEntry.IsValid()
	if err != nil {
		return 422, "", err
	}
	err = db.C.Create(&newEntry).Error
	if err != nil {
		return 500, "", err
	}
	status, err := cRedis.FlushAll(ctx).Result()
	if err != nil {
		log.Error(f+"FLUSHALL failed: ", err)
	} else {
		log.Debug(f+"FLUSHALL success: ", status)
	}
	return 200, "Success", nil
} */

/*
	code, msg, err := createQuery(f, newEntry)
	switch {
	case err != nil && code == 422:
		c.JSON(code, gin.H{"error": fmt.Sprintf("Filling errors: %v", err)})
	case err != nil && code == 500:
		log.Error(f+"failed to create entry: ", err)
		c.JSON(code, gin.H{"error": "Failed to create entry"})
	case err == nil && code == 200:
		c.JSON(code, gin.H{"message": msg})
	}
*/
