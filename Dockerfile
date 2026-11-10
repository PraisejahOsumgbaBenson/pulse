# Pulse ships as one static binary: bot, OAuth callback and scheduler.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/pulse ./cmd/bot

# Create the data directory with the runtime user's ownership before the
# volume is mounted. Docker copies this ownership into a new named volume.
RUN mkdir -p /out/data && touch /out/data/.keep && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pulse /pulse
COPY --from=build --chown=65532:65532 /out/data /data
ENV DB_PATH=/data/pulse.db OAUTH_ADDR=:8081
VOLUME ["/data"]
EXPOSE 8081
ENTRYPOINT ["/pulse"]
