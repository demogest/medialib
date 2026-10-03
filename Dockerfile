# The web server as a container: one static binary plus ffmpeg.
#   docker build -t medialib .
#   docker run -d -p 8766:8766 -e MEDIALIB_PASSWORD=change-me -v medialib-data:/data -v /path/to/videos:/media:ro medialib
FROM golang:1.27-alpine AS build
ARG VERSION=docker
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/demogest/medialib/internal/version.Version=${VERSION}" -o /medialib ./cmd/medialib

FROM alpine:3.24
RUN apk add --no-cache ffmpeg ca-certificates tzdata && adduser -D -u 10001 medialib && mkdir /data /media && chown medialib /data
COPY --from=build /medialib /usr/local/bin/medialib
USER medialib
ENV MEDIALIB_HOME=/data MEDIALIB_HOST=0.0.0.0 MEDIALIB_PORT=8766
VOLUME /data
EXPOSE 8766
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8766/healthz >/dev/null || exit 1
ENTRYPOINT ["medialib"]
CMD ["serve", "--no-browser"]
