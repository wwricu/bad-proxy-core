FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bad-proxy-core ./cmd/bad-proxy-core

FROM alpine:3.20
RUN mkdir -p /etc/bad-proxy
COPY --from=build /out/bad-proxy-core /usr/bin/bad-proxy-core
# mount your config at /etc/bad-proxy/config.json and (optionally) rules.dat
# e.g. docker run -d -v ./config.json:/etc/bad-proxy/config.json:ro \
#          -v ./rules.dat:/etc/bad-proxy/rules.dat:ro -p 443:443 bad-proxy-core
EXPOSE 443
CMD ["/usr/bin/bad-proxy-core", "--config", "/etc/bad-proxy/config.json", "--router-path", "/etc/bad-proxy/rules.dat"]
