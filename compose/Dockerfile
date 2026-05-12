FROM golang:1.26 AS build

WORKDIR /src

COPY . .
RUN go mod edit \
    -dropreplace=github.com/costa92/llm-agent \
    -dropreplace=github.com/costa92/llm-agent-otel \
    -dropreplace=github.com/costa92/llm-agent-providers
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/server

FROM gcr.io/distroless/base-debian12

WORKDIR /app
COPY --from=build /out/server /app/server

EXPOSE 8080

ENTRYPOINT ["/app/server"]
