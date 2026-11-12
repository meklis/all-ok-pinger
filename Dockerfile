FROM golang:1.22 AS builder

# set by CI from git tag, defaults from main/main.go are used if empty
ARG VERSION
ARG BUILD_DATE

WORKDIR /app
COPY . .

RUN LDFLAGS="-s -w"; \
    if [ -n "$VERSION" ]; then LDFLAGS="$LDFLAGS -X main.VERSION=$VERSION"; fi; \
    if [ -n "$BUILD_DATE" ]; then LDFLAGS="$LDFLAGS -X main.BUILD_DATE=$BUILD_DATE"; fi; \
    CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o icmp-pinger ./main

FROM debian:bullseye-slim

ENV SLEEP_INTERVAL=30s
ENV PINGER_IDENT=PINGER
ENV API_HOST_LIST_URL=http://127.0.0.1:9099/api/v1/component/pinger/pinger
ENV API_REPORTS_URL=http://127.0.0.1:9099/api/v1/component/pinger/pinger

WORKDIR /app
COPY --from=builder /app/icmp-pinger /app
COPY pinger.config.yml /app
ENTRYPOINT ["/app/icmp-pinger"]
