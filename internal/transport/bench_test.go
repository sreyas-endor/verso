package transport

// bench_test.go — the decode-side numbers PERFORMANCE_OPTIMIZATION_PLAN.md §6.2
// asks for alongside S2.
//
// The read limit and the length caps are bets that the work before validation
// is worth bounding. These measure that work directly: what one frame costs to
// unmarshal, legal and not, and how that scales with the size a client is
// allowed to choose.

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	genpb "github.com/sreyas-endor/verso/internal/gen/verso/v1"
	"github.com/sreyas-endor/verso/internal/room"
)

func mustMarshal(b *testing.B, m proto.Message) []byte {
	b.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		b.Fatal(err)
	}
	return raw
}

// BenchmarkDecodeCommand is the per-frame cost of the read loop's first act,
// across the sizes a client can choose between.
//
//	typical    one STROKE_BATCH_MS batch: what actually arrives 20 times a
//	           second from the one artist in a room.
//	maximum    a full MaxPointsPerStroke stroke: the largest legal frame, and
//	           what DefaultReadLimit is sized to fit.
//	padded     the same command inflated with a field this build has no name
//	           for. With DiscardUnknown the padding is skipped rather than
//	           retained in the message, so this is the cost of scanning past
//	           it — the difference between the two is what S2 point 4 removes
//	           from the resident cost of every decoded command.
func BenchmarkDecodeCommand(b *testing.B) {
	typical := mustMarshal(b, &genpb.ClientCommand{Cid: "a",
		Cmd: &genpb.ClientCommand_StrokePoints{StrokePoints: &genpb.StrokePoints{
			Points: make([]int32, 16)}}})
	maximum := mustMarshal(b, maxLegalCommand("a"))

	// The same maximum frame with an unknown field appended, up to the read
	// limit. A newer client's extra data looks exactly like this.
	padded := append([]byte(nil), maximum...)
	padded = append(padded, 0xFA, 0x3F) // field 1023, wire type 2
	pad := int(DefaultReadLimit) - len(padded) - 3
	padded = protowire(padded, pad)

	cases := []struct {
		name string
		raw  []byte
	}{
		{"typical", typical},
		{"maximum", maximum},
		{"padded-to-read-limit", padded},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.raw)))
			b.ReportAllocs()
			for b.Loop() {
				cmd := &genpb.ClientCommand{}
				if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(tc.raw, cmd); err != nil {
					b.Fatalf("%s: %v", tc.name, err)
				}
			}
		})
	}
}

// protowire appends a length-delimited payload of n bytes to buf.
func protowire(buf []byte, n int) []byte {
	for v := n; ; {
		if v < 0x80 {
			buf = append(buf, byte(v))
			break
		}
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, make([]byte, n)...)
}

// BenchmarkDecodeMalformed is the cost of the rejection path. A client that
// sends garbage as fast as the command limiter allows pays the server this much
// per frame, so it is the number the limiter's rate is worth reading next to.
func BenchmarkDecodeMalformed(b *testing.B) {
	// A varint field header with no body: not a decodable ClientCommand.
	raw := []byte{0x08, 0xFF}
	b.ReportAllocs()
	for b.Loop() {
		cmd := &genpb.ClientCommand{}
		if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, cmd); err == nil {
			b.Fatal("malformed frame decoded")
		}
	}
}

// BenchmarkValidate is what the length and union checks add per frame. It is
// the price of S2, and it has to be small next to the decode it protects or the
// limits cost more than the attack.
func BenchmarkValidate(b *testing.B) {
	cmds := []*genpb.ClientCommand{
		maxLegalCommand("a"),
		join(strings.Repeat("n", MaxRawNameLen), strings.Repeat("A", room.MinPlayers), ""),
		{Cmd: &genpb.ClientCommand_RequestSnapshot{RequestSnapshot: &genpb.RequestSnapshot{}}},
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if err := validate(cmds[i%len(cmds)]); err != nil {
			b.Fatal(err)
		}
	}
}
