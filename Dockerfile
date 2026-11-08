FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/axolotl-server ./cmd/axolotl-server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home axolotl
WORKDIR /srv/app
COPY --from=build /out/axolotl-server /usr/local/bin/axolotl-server
RUN mkdir -p /srv/app/dist /srv/app/instance && chown -R axolotl:axolotl /srv/app
USER axolotl
ENV APP_ENV=production UPDATE_SERVER_HOST=0.0.0.0 UPDATE_SERVER_PORT=8082 DIST_ROOT=/srv/app/dist
EXPOSE 8082
STOPSIGNAL SIGTERM
ENTRYPOINT ["axolotl-server"]
CMD ["serve"]

