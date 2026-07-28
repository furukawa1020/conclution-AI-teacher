# syntax=docker/dockerfile:1

FROM golang:1.25.5-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=readonly -trimpath \
    -ldflags="-s -w -buildid=" \
    -o /out/kotae-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=nonroot:nonroot /out/kotae-api /kotae-api

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/kotae-api"]
