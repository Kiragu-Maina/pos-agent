# syntax=docker/dockerfile:1

# ---- build stage -----------------------------------------------------------
# Go 1.20 matches the module's pinned language level (the last toolchain that
# still targets Windows 7). The build is pure Go and cgo free, so it produces a
# fully static binary with no shared library dependencies.
FROM golang:1.20-alpine AS build
WORKDIR /src

# GOWORK=off keeps this build inside the root module alone. The repo's go.work
# pulls in the cloud module, whose go directive (1.25) the 1.20 toolchain would
# reject; the local agent is self-contained (bbolt only) and needs no workspace.
ENV GOWORK=off

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pos ./cmd/pos \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakeprinter ./cmd/fakeprinter

# ---- runtime stage ---------------------------------------------------------
# Alpine keeps the image tiny while still giving a shell for debugging. tzdata
# lets the agent report the correct local day for its sales analytics.
FROM alpine:3.20
RUN apk add --no-cache tzdata wget \
 && addgroup -S pos && adduser -S -G pos -u 10001 pos \
 && mkdir -p /data && chown pos:pos /data

COPY --from=build /out/pos /usr/local/bin/pos
COPY --from=build /out/fakeprinter /usr/local/bin/fakeprinter

USER pos
WORKDIR /home/pos
VOLUME ["/data"]

# 7777 is the web UI and API; 9100 is the raw ESC/POS port the fake printer uses.
EXPOSE 7777 9100

CMD ["pos"]
