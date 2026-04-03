package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/hardik-choksi/experiments/go/grpc-demo/gen/telemetry/v1"
)

// ── 1. Logging interceptor ────────────────────────────────────────────────────
//
// A gRPC UnaryServerInterceptor has this signature:
//
//   func(ctx context.Context,
//        req any,
//        info *grpc.UnaryServerInfo,
//        handler grpc.UnaryHandler,
//   ) (any, error)
//
// It wraps every unary RPC. You get the request before the handler runs,
// and the response/error after. Same idea as HTTP middleware, but typed.

func loggingInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()

		// Call the actual RPC handler
		resp, err := handler(ctx, req)

		// Extract the gRPC status code from the error (or OK if nil)
		code := status.Code(err)
		duration := time.Since(start)

		fields := []zap.Field{
			zap.String("method", info.FullMethod),
			zap.String("code", code.String()),
			zap.Duration("duration", duration),
		}

		if err != nil {
			fields = append(fields, zap.Error(err))
			logger.Error("rpc failed", fields...)
		} else {
			logger.Info("rpc completed", fields...)
		}

		return resp, err
	}
}

// ── 2. Implement the interface protoc generated ───────────────────────────────

type telemetryServer struct {
	pb.UnimplementedTelemetryServiceServer
	logger *zap.Logger
}

func (s *telemetryServer) ExportSpans(
	ctx context.Context,
	req *pb.ExportSpansRequest,
) (*pb.ExportSpansResponse, error) {

	if len(req.Spans) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no spans provided")
	}

	accepted := 0
	for _, span := range req.Spans {
		if err := s.processSpan(span); err != nil {
			s.logger.Warn("dropping span",
				zap.String("span_id", span.SpanId),
				zap.Error(err),
			)
			continue
		}
		accepted++
	}

	s.logger.Info("batch processed",
		zap.Int("accepted", accepted),
		zap.Int("total", len(req.Spans)),
	)

	return &pb.ExportSpansResponse{
		AcceptedSpans: int32(accepted),
		Message:       fmt.Sprintf("processed batch of %d", len(req.Spans)),
	}, nil
}

func (s *telemetryServer) processSpan(span *pb.Span) error {
	if span.TraceId == "" || span.SpanId == "" {
		return fmt.Errorf("missing trace_id or span_id")
	}
	s.logger.Debug("span received",
		zap.String("service", span.Service),
		zap.String("name", span.Name),
		zap.String("status", span.Status.String()),
	)
	return nil
}

// ── 3. Wire up the gRPC server ────────────────────────────────────────────────

func main() {
	// zap.NewProduction() outputs JSON — grep-friendly, ready for log pipelines.
	// zap.NewDevelopment() outputs human-readable console format with colors.
	logger, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("failed to init logger: %v", err))
	}
	defer logger.Sync()

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		logger.Fatal("failed to listen", zap.Error(err))
	}

	// ChainUnaryInterceptor runs interceptors in order.
	// You can stack more: auth, tracing, recovery, etc.
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			loggingInterceptor(logger),
		),
	)

	pb.RegisterTelemetryServiceServer(srv, &telemetryServer{logger: logger})

	logger.Info("gRPC server listening", zap.String("addr", ":50051"))
	if err := srv.Serve(lis); err != nil {
		logger.Fatal("failed to serve", zap.Error(err))
	}
}
