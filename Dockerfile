FROM golang:1.22 AS builder

WORKDIR /app
COPY . .

RUN CGO_ENABLED=0 go build -o icmp-pinger ./main/main.go

FROM debian:bullseye-slim

ENV SLEEP_AFTER_CHECK=10s
ENV PINGER_IDENT=PINGER
ENV API_HOST_LIST_URL=http://127.0.0.1:9099/api/v1/component/pinger/pinger
ENV API_REPORTS_URL=http://127.0.0.1:9099/api/v1/component/pinger/pinger

WORKDIR /app
COPY --from=builder /app/icmp-pinger /app
COPY pinger.config.yml /app
ENTRYPOINT ["/app/icmp-pinger"]