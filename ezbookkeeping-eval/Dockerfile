# Build the cgo-enabled backend for the image target architecture. SQLite uses
# github.com/mattn/go-sqlite3, so the build must retain CGO_ENABLED=1.
FROM --platform=$TARGETPLATFORM golang:1.26.4-alpine3.24 AS be-builder
ARG TARGETOS
ARG TARGETARCH
ARG VCS_REF=unknown
WORKDIR /src
RUN apk add --no-cache gcc g++ libc-dev git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.CommitHash=${VCS_REF}" -o /out/ezbookkeeping .

# Build frontend files on the native builder architecture. The output is
# static and is copied into both the amd64 and arm64 runtime images.
FROM --platform=$BUILDPLATFORM node:24.18.0-alpine3.24 AS fe-builder
ARG VCS_REF=unknown
ENV BUILD_COMMIT_HASH=$VCS_REF
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# Package the platform-specific image.
FROM --platform=$TARGETPLATFORM alpine:3.24.1
LABEL maintainer="MaysWind <i@mayswind.net>"
RUN addgroup -S -g 1000 ezbookkeeping && adduser -S -G ezbookkeeping -u 1000 ezbookkeeping
RUN apk --no-cache add ca-certificates tzdata poppler-utils wget
COPY docker/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
RUN mkdir -p /ezbookkeeping && chown 1000:1000 /ezbookkeeping \
  && mkdir -p /ezbookkeeping/data && chown 1000:1000 /ezbookkeeping/data \
  && mkdir -p /ezbookkeeping/log && chown 1000:1000 /ezbookkeeping/log \
  && mkdir -p /ezbookkeeping/storage && chown 1000:1000 /ezbookkeeping/storage
WORKDIR /ezbookkeeping
COPY --from=be-builder --chown=1000:1000 /out/ezbookkeeping /ezbookkeeping/ezbookkeeping
COPY --from=fe-builder --chown=1000:1000 /src/dist /ezbookkeeping/public
COPY --chown=1000:1000 conf /ezbookkeeping/conf
COPY --chown=1000:1000 templates /ezbookkeeping/templates
COPY --chown=1000:1000 LICENSE /ezbookkeeping/LICENSE
# A container must listen on its own network interface. Host-side exposure is
# controlled by Compose's BIND_ADDRESS setting.
RUN sed -i 's/^http_addr = .*/http_addr = 0.0.0.0/' /ezbookkeeping/conf/ezbookkeeping.ini
USER 1000:1000
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/desktop || exit 1
ENTRYPOINT ["/docker-entrypoint.sh"]
