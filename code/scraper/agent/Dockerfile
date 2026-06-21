# build binary
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./ 
RUN go mod download

# copy generated proto binary and agent source
COPY gen/ ./gen/
COPY utils/utils.go ./utils/utils.go
COPY agent/main.go ./agent/main.go


RUN CGO_ENABLED=0 GOOS=linux go build -o /agent ./agent

# build the application
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /agent /agent
ENTRYPOINT ["/agent"]
