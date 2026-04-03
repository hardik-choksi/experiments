package main

import (
	"log"
	"rabbitmq-tut/utils"
)

func main() {
	connObj := utils.NewRMQConn(utils.Credentials{
		User: "user",
		Pass: "password",
		Host: "localhost",
		Port: "5672",
	})
	defer connObj.CloseConnection()

	q := connObj.QueueDeclare(utils.QueueOptions{
		Name:       "hello",
		Durability: false,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       nil,
	})

	msgs := connObj.Consume(q.Name, true)

	forever := make(chan struct{})

	go func() {
		for d := range msgs {
			log.Printf("recieved a message: %q\n", d.Body)
		}
	}()

	log.Printf(" [*] Waiting for messages. To exit press CTRL+C")

	<-forever
}
