# syntax=docker/dockerfile:1
# The Battle.net web tier: geo, telemetry-in and account for every Blizzard
# title a redirected console runs. Plain HTTP inside the pod network — Traefik
# terminates the guest's TLS for the three hostnames, the same arrangement as
# the among-us front.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/battlenet ./cmd/battlenet

FROM gcr.io/distroless/static
COPY --from=build /out/battlenet /battlenet
USER nonroot:nonroot
EXPOSE 21500 21501
ENTRYPOINT ["/battlenet"]
