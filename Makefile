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
#
# --concurrency and -max-conns are a PAIR, and the application cap is the lower
# of the two on purpose. Every live WebSocket occupies a Cloud Run request slot
# for the whole match, so at --concurrency=200 the platform stops admitting
# requests at 200 — including the health check, the handshake of a player
# reconnecting after a dropped socket, and the static asset fetches of anyone
# loading the page. Hitting that ceiling looks like the service being down.
#
# -max-conns=180 makes the application refuse the 181st socket itself, with a
# 1013 Try Again Later close the client understands, and leaves 20 slots of
# headroom for everything that is not a game socket. The margin is 10%: with
# room.MaxPlayers at 10 that is two full rooms' worth of simultaneous reconnect,
# which is what a wifi blip on a shared network actually looks like.
#
# --set-env-vars=GOMEMLIMIT is a soft limit, so it makes the collector work
# harder as the heap approaches it rather than killing the process — which is
# exactly what --memory=2Gi does instead, without warning and without a chance
# to shed load. 1700MiB leaves ~350 MiB for the Go runtime's own off-heap
# structures, goroutine stacks, and the socket buffers the kernel keeps per
# connection, none of which GOMEMLIMIT accounts for.
#
# TREAT 1700MiB AS A STARTING POINT, NOT A SETTING. It is the value
# PERFORMANCE_OPTIMIZATION_PLAN.md S5 nominates for testing, and it has not yet
# been checked against sustained load: the numbers it needs are live heap and GC
# CPU under the §6.2 scenario of 20 rooms x 10 clients. Raise or lower it from
# that measurement, not from this comment.
#
# --cpu is 4, not 1, and that is the flag that matters most here. Go reads the
# cgroup quota to pick GOMAXPROCS, so --cpu=1 makes the whole server
# single-threaded: every write pump, every room actor and the garbage collector
# timeshare one P, and any one slow operation — a Snapshot marshal, a GC cycle —
# becomes head-of-line blocking for every player in every room. It is invisible
# on a dev machine with 8-12 cores and very visible on a deployed instance.
# --max-instances=1 is forced by the architecture, so vertical is the only
# direction available.
#
# --session-affinity is deliberately absent: with exactly one instance there is
# nothing to be affine to, and it costs a cookie and a proxy lookup per
# handshake.
# Depends on `web` for the same reason `docker` does: Cloud Build runs the same
# Dockerfile and has no registry token either.
deploy: web
	@test -n "$(PROJECT)" || { echo "PROJECT is empty: pass PROJECT=... or run gcloud config set project"; exit 1; }
	gcloud run deploy $(SERVICE) \
	  --project=$(PROJECT) \
	  --region=$(REGION) \
	  --source=. \
	  --allow-unauthenticated \
	  --cpu=4 \
	  --memory=2Gi \
	  --min-instances=1 \
	  --max-instances=1 \
	  --no-cpu-throttling \
	  --concurrency=200 \
	  --set-env-vars=GOMEMLIMIT=1700MiB \
	  --args=-max-conns=180 \
	  --timeout=3600

clean:
	rm -rf $(EMBED_DIST) $(WEB_DIST) $(BIN)
