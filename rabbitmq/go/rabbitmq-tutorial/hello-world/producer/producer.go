package main

import (
	"context"
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
		Name:       "hello",
		Durability: false,
		AutoDelete: false,
		Exclusive:  false,
		NoWait:     false,
		Args:       nil,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connObj.PublishWithContext(ctx, "Hello World!", utils.PublishOptions{
		ExchangeName:   "",
		RoutingKey:     q.Name,
		Mandatory:      false,
		Immediate:      false,
		MsgPersistency: false,
	})
}
