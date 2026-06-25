FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o bollard .

FROM scratch
COPY --from=builder /build/bollard /bollard
ENTRYPOINT ["/bollard"]
