FROM golang:1 AS build

WORKDIR /src
COPY terminal/go.mod terminal/go.mod
COPY terminal/*.go terminal/
COPY week6/go.mod week6/go.mod
COPY week6/*.go week6/
COPY d30/server/go.mod d30/server/go.mod
COPY d30/server/*.go d30/server/
RUN CGO_ENABLED=0 go build -C d30/server -o /mrkai-server .

FROM scratch
COPY --from=build /mrkai-server /mrkai-server
USER 65532:65532
ENTRYPOINT ["/mrkai-server"]
