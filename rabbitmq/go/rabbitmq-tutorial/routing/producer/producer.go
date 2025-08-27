package main

import (
	"context"
	"log"
	"os"
	"rabbitmq-tut/utils"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

func main() {
	connObj := utils.NewRMQConn(utils.Credentials{
		User: "user",
		Pass: "password",
		Host: "localhost",
		Port: "5672",
	})
	defer connObj.CloseConnection()

	ch := connObj.GetChannel()

	err := ch.ExchangeDeclare(
		"logs_direct", // name
		"direct",      // type
		true,          // durable
		false,         // auto-deleted
		false,         // internal
		false,         // no-wait
		nil,           // arguments
	)
	utils.FailOnError(err, "Failed to declare an exchange")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body := utils.BodyFrom(os.Args)
	err = ch.PublishWithContext(ctx,
		"logs_direct",               // exchange
		utils.SeverityFrom(os.Args), // routing key
		false,                       // mandatory
		false,                       // immediate
		amqp091.Publishing{
			ContentType: "text/plain",
			Body:        []byte(body),
		})
	utils.FailOnError(err, "failed to publish the message")

	log.Printf(" [x] Sent %s", body)
}
