<!-- a doc that demonstrates this work-queue and how acks work and if ack is not send and tcp connection is lost with a consumer, how rabbitmq handles it -->
# Work queue

Reference: https://www.rabbitmq.com/tutorials/tutorial-two-go

---

you can fire-up 2 work queue and rabbitmq will use round-robin to distribute tasks between workers.

Workers will have to send ack (auto-ack = false) to rmq server, else server will requeue them in next 30 mins to another worker.

If a worker looses its tcp connection with rmq server without sending acks, then rmq will send those messages to another live workers.
