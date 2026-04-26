# build binary
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./ 
RUN go mod download

COPY *.go ./ 

RUN CGOENABLED=0 GOOS=linux go build -o main .

# build the application
FROM alpine:latest
WORKDIR /root/

COPY --from=builder /app/main .

CMD ["./main"]
