# Two stages: Go compiles the server around a client that was built earlier, and
# the image that ships is a distroless binary with no interpreter and no shell.
#
# The client is NOT built here. Every entry in web/package-lock.json resolves
# through an authenticated private registry, which answers 401 without a token —
# so `npm ci` cannot succeed inside an image build, locally or in Cloud Build.
# The client is therefore built on a machine that already holds that token
# (`make web`) and its output is copied in as an artifact. `make build` and
# `make deploy` both run that step first; `make docker` refuses without it.

# --- stage 1: the server -----------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# web/dist is the client built on the host. It lands where go:embed expects it.
# .dockerignore excludes cmd/verso/dist so the committed placeholder cannot
# leave a previous build's hashed assets alongside these.
COPY web/dist ./cmd/verso/dist

# Fail loudly rather than embedding an empty directory: go:embed accepts one and
# the server would then serve a 404 for its own index.html.
RUN test -f cmd/verso/dist/index.html || \
      (echo "cmd/verso/dist/index.html missing — run 'make web' before building the image" >&2; exit 1)

# CGO off and a stripped binary: the final stage has no libc to link against.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /verso ./cmd/verso

# --- stage 2: what actually ships -------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /verso /verso

# Documentation only. Cloud Run injects $PORT and cmd/verso/main.go binds it.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/verso"]
