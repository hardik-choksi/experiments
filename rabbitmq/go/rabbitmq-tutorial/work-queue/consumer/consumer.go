package main

import (
	"bytes"
	"log"
	"rabbitmq-tut/utils"
	"strings"
	"sync/atomic"
	"time"
)

var id atomic.Uint64

func init() {
	id.Store(1)
	log.Println("init method")
}

func main() {
	connObj := utils.NewRMQConn(utils.Credentials{
		User: "user",
		Pass: "password",
		Host: "localhost",
		Port: "5672",
	})
	defer connObj.CloseConnection()

	ch := connObj.GetChannel()

	err := ch.Qos(1, 0, false)
	utils.FailOnError(err, "failed to set QoS")

	q := connObj.QueueDeclare(utils.QueueOptions{
		Name:       "task_queue",
		Durability: true,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       nil,
	})

	msgs := connObj.Consume(q.Name, false)

	forever := make(chan struct{})

	go func() {
		for d := range msgs {
			v := id.Load()
			log.Printf("recieved a message: %q, assigned id: %d\n", d.Body, v)

			dotCount := bytes.Count(d.Body, []byte("."))
			time.Sleep(time.Duration(dotCount) * time.Second)

			log.Printf("message %d done after %d seconds of wait!", v, dotCount)

			id.Add(1)
			if !strings.Contains(string(d.Body), "no ack") {
				d.Ack(false)
			}
		}
	}()

	log.Printf(" [*] Waiting for messages. To exit press CTRL+C")

	<-forever
}
