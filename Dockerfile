# syntax=docker/dockerfile:1

###############################################################################
# cloudscraper-go — static binary in a distroless image.
#
#   docker build -t cloudscraper-go .
#   docker run --rm cloudscraper-go fingerprint --profile chrome
#   docker run --rm cloudscraper-go fetch https://example.com
###############################################################################

# --- build ---------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static, stripped binary — no libc needed, so it runs on distroless/scratch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cloudscraper ./cmd/cloudscraper

# --- runtime -------------------------------------------------------------
# distroless carries CA certificates (needed for TLS) and a nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cloudscraper /usr/local/bin/cloudscraper
USER nonroot:nonroot
ENTRYPOINT ["cloudscraper"]
CMD ["--help"]
