# Verso

Verso is a real-time browser drawing game for 3–10 players. Everyone receives
a secret word, except one player receives a related one; draw shared clues,
discuss away from the app, and vote anonymously.

## Run it

Requirements: Go (the module selects its required toolchain automatically),
Node/npm, and `buf` only when regenerating protobuf code.

```sh
cd web && npm install && npm run build
cd .. && make build
./verso
```

Open `http://localhost:8080`, create a room, and share its link. During client
development, run `go run ./cmd/verso -dev -webroot web/dist` alongside
`cd web && npm run dev`.

## Checks

```sh
make test
go test ./... -race
make gen       # after changing proto/verso/v1/game.proto
```

`make build` copies the Vite output into `cmd/verso/dist`, which is embedded in
the release binary. The placeholder in that directory keeps `go build` usable
before the first client build.
