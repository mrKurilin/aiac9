# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src/d35
COPY --from=terminal . /src/terminal/
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /mrkai .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 bot && mkdir /data && chown bot:bot /data
COPY --from=build /mrkai /usr/local/bin/mrkai
USER bot
ENV DATA_DIR=/data
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 CMD ["mrkai", "--healthcheck"]
ENTRYPOINT ["mrkai"]
CMD ["--telegram"]
