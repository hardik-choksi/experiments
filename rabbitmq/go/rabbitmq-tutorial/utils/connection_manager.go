package utils

import (
	"context"
	"fmt"
	"log"

	"github.com/rabbitmq/amqp091-go"
)

type Credentials struct {
	User string
	Pass string
	Host string
	Port string
}

type RabbitMQConn struct {
	conn    *amqp091.Connection
	channel *amqp091.Channel
	queue   amqp091.Queue
}

func NewRMQConn(c Credentials) RabbitMQConn {
	connObj := RabbitMQConn{}
	connObj.ConnectRMQ(c.User, c.Pass, c.Host, c.Port)
	return connObj
}

func (rm *RabbitMQConn) ConnectRMQ(user, pass, host, port string) {
	url := fmt.Sprintf("amqp://%s:%s@%s:%s/", user, pass, host, port)
	conn, err := amqp091.Dial(url)
	FailOnError(err, "Failed to connect to RabbitMQ")
	rm.conn = conn

	ch, err := conn.Channel()
	FailOnError(err, "failed to open channel")
	rm.channel = ch
}

func (rm *RabbitMQConn) CloseConnection() {
	if rm.conn == nil {
		log.Println("cannot close nil connection")
		return
	}
	err := rm.channel.Close()
	FailOnError(err, "failed to close channel")

	err = rm.conn.Close()
	FailOnError(err, "failed to close channel connection")
}

func (rm *RabbitMQConn) GetChannel() *amqp091.Channel {
	if rm.channel == nil {
		FailOnError(fmt.Errorf("channel is nil"), "failed to get channel")
	}
	return rm.channel
}

type QueueOptions struct {
	Name       string        // if empty, a random name will be generated
	Durability bool          // true if we want the queue to survive a broker restart
	AutoDelete bool          // true if we want the queue to be deleted when there are no more consumers on it
	Exclusive  bool          // true if we want the queue to be used by only one connection and deleted when that connection closes
	NoWait     bool          // true if we don't want to wait for a server response and the queue will be created asynchronously
	Args       amqp091.Table // additional arguments for the queue
}

func (rm *RabbitMQConn) QueueDeclare(opts QueueOptions) amqp091.Queue {
	q, err := rm.channel.QueueDeclare(
		opts.Name,
		opts.Durability,
		opts.AutoDelete,
		opts.Exclusive,
		opts.NoWait,
		opts.Args,
	)
	FailOnError(err, "failed to declare a queue")

	rm.queue = q
	return rm.queue
}

type PublishOptions struct {
	ExchangeName   string // name of the exchange to publish to
	RoutingKey     string // routing key or queue name to publish to
	Mandatory      bool   // true if we want the server to return an unroutable message with a Return method
	Immediate      bool   // true if we want the server to return an undeliverable message with a Return method
	MsgPersistency bool   // true if we want the message to be persistent
}

func (rm *RabbitMQConn) PublishWithContext(ctx context.Context, message string, publishOpts PublishOptions) {
	opts := amqp091.Publishing{
		ContentType: "text/plain",
		Body:        []byte(message),
	}

	if publishOpts.MsgPersistency {
		opts.DeliveryMode = amqp091.Persistent
	}

	err := rm.channel.PublishWithContext(
		ctx, publishOpts.ExchangeName,
		publishOpts.RoutingKey,
		publishOpts.Mandatory,
		publishOpts.Immediate, opts)

	FailOnError(err, "failed to publish the message")
	log.Printf(" [x] Sent %q\n", message)
}

type ConsumeOptions struct {
	QueueName string // name of the queue to consume from
	AutoAck   bool   // true if we want the server to consider messages acknowledged once delivered
}

func (rm *RabbitMQConn) Consume(queueName string, autoAck bool) <-chan amqp091.Delivery {
	msgs, err := rm.channel.Consume(queueName, "", autoAck, false, false, false, nil)
	if err != nil {
		FailOnError(err, "failed to consume the message")
	}
	return msgs
}
