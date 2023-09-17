package kafka

import (
	"os"
	"people/logging"
	"strings"

	"github.com/IBM/sarama"
	_ "github.com/joho/godotenv/autoload"
)

var (
	log     = logging.Config
	address []string
)

// The function initializes the Apache Kafka connection data from the
// environment variables and triggers the creation of topics.
func Start(topics Topics) {
	address = strings.Split(os.Getenv("AK_ADDR"), ",")
	topics.Create()
}

type Topics []Topic

// The method creates Apache Kafka topics based on structure data.
func (args Topics) Create() {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	client, err := sarama.NewClient(address, config)
	if err != nil {
		log.Fatal("Failed to create client: ", err)
	}
	defer client.Close()
	admin, err := sarama.NewClusterAdminFromClient(client)
	if err != nil {
		log.Fatal("Failed to create admin client: ", err)
	}
	defer admin.Close()
	for _, v := range args {
		topicDetail := &sarama.TopicDetail{
			NumPartitions:     v.Partitions,
			ReplicationFactor: v.Replication,
		}
		topicName := v.Name
		err = admin.CreateTopic(topicName, topicDetail, false)
		if err != nil {
			log.Infof("Topic creating info: %s", err)
		} else {
			log.Infof("Topic '%s' created.", topicName)
		}
	}
}

type Topic struct {
	Name        string
	Partitions  int32
	Replication int16
}

// The method creates a consumer and consume of the Apache Kafka
// messages.
func (arg Topic) Consume(data chan []byte) {
	config := sarama.NewConfig()
	config.Consumer.Return.Errors = true
	consumer, err := sarama.NewConsumer(address, config)
	if err != nil {
		log.Fatalf("Failed to create consumer: %v", err)
	}
	reader, err := consumer.ConsumePartition(
		arg.Name, arg.Partitions-1, sarama.OffsetNewest,
	)
	if err != nil {
		log.Fatalf("Failed to create ConsumePartition %s: %v", arg.Name, err)
	}
	defer reader.Close()
	log.Infof("Awaiting data from %s...", arg.Name)
	for {
		select {
		case msg := <-reader.Messages():
			data <- msg.Value
			log.Debugf("%s message: %v\n", arg.Name, msg)
		case err := <-reader.Errors():
			log.Errorf("%s error consuming message: %v\n", arg.Name, err)
		}
	}
}

// The function create an async producer of the Apache Kafka messages.
func NewProd() sarama.AsyncProducer {
	config := sarama.NewConfig()
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Partitioner = sarama.NewManualPartitioner
	config.Producer.Return.Successes = true
	client, err := sarama.NewClient(address, config)
	if err != nil {
		log.Fatal("Failed to create client: ", err)
	}
	producer, err := sarama.NewAsyncProducerFromClient(client)
	if err != nil {
		log.Fatal("Failed to create producer from client: ", err)
	}
	return producer
}

// The method for produce a message to the topic.
func (arg Topic) Produce(val []byte, prod sarama.AsyncProducer) string {
	message := &sarama.ProducerMessage{
		Topic:     arg.Name,
		Value:     sarama.ByteEncoder(val),
		Partition: arg.Partitions - 1,
	}
	prod.Input() <- message
	select {
	case success := <-prod.Successes():
		log.Debugf(
			"Message sent to partition %d at offset %d\n",
			success.Partition,
			success.Offset,
		)
		return "Message sent successfully"
	case err := <-prod.Errors():
		log.Error("Failed to sent message: ", err)
		return "Message sent unsuccessfully"
	}
}
