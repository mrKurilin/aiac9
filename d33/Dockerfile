# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY --from=terminal *.go go.mod ./terminal/
COPY go.mod go.sum ./d33/
WORKDIR /src/d33
RUN go mod download
COPY *.go ./
COPY web/ ./web/
COPY docs/faq.md docs/product.md ./docs/
COPY README.md ./
COPY support/tickets.json ./support/tickets.json
RUN go test ./... && go vet ./... && go build -trimpath -ldflags='-s -w' -o /out/mrkai .
RUN cd /src/terminal && go test ./...
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=build /out/mrkai /app/mrkai
COPY --from=build /src/d33/README.md /app/README.md
COPY --from=build /src/d33/docs/ /app/docs/
COPY --from=build /src/d33/support/ /app/support/
COPY --from=build --chown=65532:65532 /out/data /app/data
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/app/mrkai"]
CMD ["web"]
