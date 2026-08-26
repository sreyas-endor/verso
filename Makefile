.PHONY: build web dev test gen docker deploy clean

WEB_DIST := web/dist
EMBED_DIST := cmd/verso/dist
BIN := verso

# Cloud Run target. Override on the command line:
#   make deploy PROJECT=my-proj REGION=asia-south1
PROJECT ?= $(shell gcloud config get-value project 2>/dev/null)
REGION  ?= us-central1
SERVICE ?= verso

build: web
	# The embedded tree is rebuilt, not merged into. Vite fingerprints its
	# output, so copying over an existing dist/ leaves every previous build's
	# hashed assets behind and go:embed bakes all of them into the binary.
	rm -rf $(EMBED_DIST)
	mkdir -p $(EMBED_DIST)
	cp -R $(WEB_DIST)/. $(EMBED_DIST)/
	go build -o $(BIN) ./cmd/verso

web:
	cd web && npm run build

dev:
	@echo "Run 'make web' once, then use these in separate terminals:"
	@echo "  go run ./cmd/verso -dev -webroot web/dist"
	@echo "  cd web && npm run dev"

test:
	go test ./... -count=1
	cd web && npm run typecheck

gen:
	buf generate

# The image build cannot run npm: web/package-lock.json resolves every package
# through an authenticated private registry, and no build environment holds that
# token. So the client is built here, on a machine that does, and the image
# copies web/dist in as a finished artifact.
docker: web
	docker build -t $(SERVICE):local .

# Room state lives in the process, so the service is pinned to exactly one
# always-warm instance:
#   --max-instances=1     a second instance would hold a disjoint set of rooms
#   --min-instances=1     a cold start drops every live WebSocket
#   --no-cpu-throttling   phase timers have to run between requests
#   --concurrency         the default 80 caps the whole server at 80 sockets
#   --timeout=3600        a WebSocket request lives as long as the match
# Depends on `web` for the same reason `docker` does: Cloud Build runs the same
# Dockerfile and has no registry token either.
deploy: web
	@test -n "$(PROJECT)" || { echo "PROJECT is empty: pass PROJECT=... or run gcloud config set project"; exit 1; }
	gcloud run deploy $(SERVICE) \
	  --project=$(PROJECT) \
	  --region=$(REGION) \
	  --source=. \
	  --allow-unauthenticated \
	  --cpu=1 \
	  --memory=512Mi \
	  --min-instances=1 \
	  --max-instances=1 \
	  --no-cpu-throttling \
	  --concurrency=200 \
	  --timeout=3600 \
	  --session-affinity

clean:
	rm -rf $(EMBED_DIST) $(WEB_DIST) $(BIN)
