# syntax=docker/dockerfile:1

FROM --platform=$TARGETPLATFORM golang:1.26.4-alpine3.24 AS backend-builder
ARG TARGETOS
ARG TARGETARCH
ARG VCS_REF=unknown
WORKDIR /src
RUN apk add --no-cache gcc g++ libc-dev git
COPY ezbookkeeping-eval/go.mod ezbookkeeping-eval/go.sum ./
RUN go mod download
COPY ezbookkeeping-eval/ ./
RUN CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.CommitHash=${VCS_REF}" \
    -o /out/ezbookkeeping .

FROM --platform=$BUILDPLATFORM node:24.18.0-alpine3.24 AS frontend-builder
ARG VCS_REF=unknown
ENV BUILD_COMMIT_HASH=$VCS_REF
WORKDIR /src
COPY ezbookkeeping-eval/package.json ezbookkeeping-eval/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ezbookkeeping-eval/ ./
RUN npm run build

FROM alpine:3.24.1
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="ezbook 魔改版" \
      org.opencontainers.image.description="Personal-finance application with reconciliation and cash-flow extensions" \
      org.opencontainers.image.revision=$VCS_REF
RUN addgroup -S -g 1000 ezbookkeeping \
    && adduser -S -G ezbookkeeping -u 1000 ezbookkeeping \
    && apk --no-cache add ca-certificates tzdata poppler-utils wget \
    && mkdir -p /ezbookkeeping/data /ezbookkeeping/log /ezbookkeeping/storage \
    && chown -R 1000:1000 /ezbookkeeping
COPY --from=backend-builder --chown=1000:1000 /out/ezbookkeeping /ezbookkeeping/ezbookkeeping
COPY --from=frontend-builder --chown=1000:1000 /src/dist /ezbookkeeping/public
COPY --chown=1000:1000 ezbookkeeping-eval/docker/docker-entrypoint.sh /docker-entrypoint.sh
COPY --chown=1000:1000 ezbookkeeping-eval/conf /ezbookkeeping/conf
COPY --chown=1000:1000 ezbookkeeping-eval/templates /ezbookkeeping/templates
COPY --chown=1000:1000 ezbookkeeping-eval/LICENSE /ezbookkeeping/LICENSE
RUN chmod 0555 /docker-entrypoint.sh \
    && sed -i 's/^http_addr = .*/http_addr = 0.0.0.0/' /ezbookkeeping/conf/ezbookkeeping.ini
WORKDIR /ezbookkeeping
USER 1000:1000
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/desktop || exit 1
ENTRYPOINT ["/docker-entrypoint.sh"]
