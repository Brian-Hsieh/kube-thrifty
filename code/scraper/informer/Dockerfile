FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./ 
RUN go mod download

# copy generated protobuf and informer source
COPY gen/ ./gen/
COPY informer/main.go ./informer/main.go


RUN CGO_ENABLED=0 GOOS=linux go build -o /informer ./informer

# build the application
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /informer /informer
ENTRYPOINT ["/informer"]
