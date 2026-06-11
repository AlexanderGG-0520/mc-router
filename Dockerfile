# syntax=docker/dockerfile:1

ARG BUILDPLATFORM=linux/amd64
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/mc-gateway ./cmd/mc-gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mc-gateway /mc-gateway
USER nonroot:nonroot
EXPOSE 25565/tcp 9090/tcp
ENTRYPOINT ["/mc-gateway"]
CMD ["-config", "/etc/mc-gateway/config.yaml"]
