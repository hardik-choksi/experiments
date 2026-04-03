package utils

import (
	"log"
	"os"
	"strings"
)

/*
	FailOnError is a helper function to check the error and log it with a message

and stop the program execution.
*/
func FailOnError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}

/*
	BodyFrom returns the message body from command line arguments

or "hello" as the default message.
example usage: go run producer.go "Hello World!"
*/
func BodyFrom(args []string) string {
	var s string
	if (len(args) < 2) || os.Args[1] == "" {
		s = "hello"
	} else {
		s = strings.Join(args[1:], " ")
	}
	return s
}

/*
	SeverityFrom returns the severity level from command line arguments

or "info" as the default severity level.
example usage: go run producer.go warning "This is a warning message"
*/
func SeverityFrom(args []string) string {
	var s string
	if (len(args) < 2) || os.Args[1] == "" {
		s = "info"
	} else {
		s = os.Args[1]
	}
	return s
}
