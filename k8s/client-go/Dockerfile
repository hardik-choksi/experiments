FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o main .

FROM alpine:3.23.5

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /app/main .

ENTRYPOINT [ "./main" ]
