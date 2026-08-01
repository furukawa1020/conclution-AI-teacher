# syntax=docker/dockerfile:1

FROM golang:1.25.5-bookworm@sha256:d9132cce84391efab786495288756d60e1da215b1f94e87860aeefc3d4c45b6d AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=readonly -trimpath \
    -ldflags="-s -w -buildid=" \
    -o /out/kotae-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

COPY --from=build --chown=nonroot:nonroot /out/kotae-api /kotae-api

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/kotae-api"]
