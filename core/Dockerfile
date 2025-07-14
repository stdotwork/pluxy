FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o ./CLIProxyAPI ./cmd/server/

FROM alpine:3.22.0

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

WORKDIR /CLIProxyAPI

EXPOSE 8317

CMD ["./CLIProxyAPI"]