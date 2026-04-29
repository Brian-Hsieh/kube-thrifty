# build binary
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./ 
RUN go mod download

# copy generated proto binary and agent source
COPY gen/ ./gen/
COPY agent/main.go ./agent/main.go


RUN CGOENABLED=0 GOOS=linux go build -o /agent ./agent

# build the application
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /agent /agent
ENTRYPOINT ["/agent"]
