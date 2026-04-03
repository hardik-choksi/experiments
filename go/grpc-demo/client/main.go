package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/hardik-choksi/experiments/go/grpc-demo/gen/telemetry/v1"
)

func main() {
	// Dial the gRPC server (no TLS for local dev)
	conn, err := grpc.NewClient("localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewTelemetryServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build a sample ExportSpans request
	now := time.Now().UnixNano()
	req := &pb.ExportSpansRequest{
		Spans: []*pb.Span{
			{
				TraceId:   "trace-001",
				SpanId:    "span-001",
				Name:      "http.GET /api",
				Service:   "client-demo",
				StartTime: now - int64(time.Millisecond),
				EndTime:   now,
				Status:    pb.SpanStatus_SPAN_STATUS_OK,
			},
			{
				TraceId:   "trace-001",
				SpanId:    "span-002",
				Name:      "db.query",
				Service:   "client-demo",
				StartTime: now - int64(2*time.Millisecond),
				EndTime:   now - int64(time.Millisecond),
				Status:    pb.SpanStatus_SPAN_STATUS_OK,
			},
		},
	}

	resp, err := client.ExportSpans(ctx, req)
	if err != nil {
		log.Fatalf("ExportSpans failed: %v", err)
	}

	fmt.Printf("Response: accepted=%d message=%q\n",
		resp.AcceptedSpans, resp.Message)
}
