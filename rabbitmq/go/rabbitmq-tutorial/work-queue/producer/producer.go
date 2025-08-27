package main

import (
	"context"
	"os"
	"rabbitmq-tut/utils"
	"time"
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
		Name:       "task_queue",
		Durability: true,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       nil,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body := utils.BodyFrom(os.Args)

	connObj.PublishWithContext(ctx, body, utils.PublishOptions{
		ExchangeName:   "",
		RoutingKey:     q.Name,
		Mandatory:      true,
		Immediate:      false,
		MsgPersistency: true,
	})
}
