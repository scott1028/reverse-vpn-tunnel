package tunnel

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
)

type packetReadTUN struct {
	tun.Device
	packets [][]byte
	err     error
}

func (r *packetReadTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for i, packet := range r.packets {
		sizes[i] = copy(bufs[i][offset:], packet)
	}
	return len(r.packets), r.err
}

func TestGoodbyeReadBeforeShutdown(t *testing.T) {
	var source tun.Device = &packetReadTUN{packets: [][]byte{getGoodbyePacket()}}
	source = &goodbyeReadFilter{Device: source, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), allowGoodbye: &atomic.Bool{}}
	bufs, sizes := [][]byte{make([]byte, 128)}, []int{0}
	if n, err := source.Read(bufs, sizes, 16); n != 1 || err != nil || sizes[0] != 0 {
		t.Fatalf("goodbye before shutdown was not suppressed: n=%d, size=%d, err=%v", n, sizes[0], err)
	}
}

func TestGoodbyeReadMixedBatch(t *testing.T) {
	wrongSource := getGoodbyePacket()
	wrongSource[12] = 192
	packets := [][]byte{{0x60, 1, 2}, getGoodbyePacket(), nil, wrongSource, getGoodbyePacket()[:19], append(getGoodbyePacket(), 0), getGoodbyePacket()}
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "allowed"}[allowed], func(t *testing.T) {
			var permission atomic.Bool
			permission.Store(allowed)
			filter := &goodbyeReadFilter{Device: &packetReadTUN{packets: packets}, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), allowGoodbye: &permission}
			bufs, sizes := make([][]byte, len(packets)+1), make([]int, len(packets)+1)
			for i := range bufs {
				bufs[i] = bytes.Repeat([]byte{0xa5}, 128)
				sizes[i] = 777
			}
			if n, err := filter.Read(bufs, sizes, 16); n != len(packets) || err != nil {
				t.Fatalf("Read = %d, %v", n, err)
			}
			for i, packet := range packets {
				wantSize := len(packet)
				if !allowed && (i == 1 || i == 6) {
					wantSize = 0
				}
				if sizes[i] != wantSize || !bytes.Equal(bufs[i][16:16+len(packet)], packet) || !bytes.Equal(bufs[i][:16], bytes.Repeat([]byte{0xa5}, 16)) {
					t.Fatalf("packet %d changed unexpectedly: size %d, want %d", i, sizes[i], wantSize)
				}
			}
			if sizes[len(packets)] != 777 || !reflect.DeepEqual(bufs[len(packets)], bytes.Repeat([]byte{0xa5}, 128)) {
				t.Fatal("Read changed an entry past n")
			}
		})
	}
}

func TestGoodbyeReadPreservesError(t *testing.T) {
	readErr := errors.New("read failed after one packet")
	filter := &goodbyeReadFilter{Device: &packetReadTUN{packets: [][]byte{getGoodbyePacket()}, err: readErr}, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), allowGoodbye: &atomic.Bool{}}
	sizes := []int{0}
	if n, err := filter.Read([][]byte{make([]byte, 128)}, sizes, 16); n != 1 || err != readErr || sizes[0] != 0 {
		t.Fatalf("Read = %d, %v; size %d", n, err, sizes[0])
	}
}

func TestGoodbyeReadDoesNotAllocate(t *testing.T) {
	packet := getGoodbyePacket()
	packet[len(packet)-1] = '2'
	filter := &goodbyeReadFilter{Device: &packetReadTUN{packets: [][]byte{packet}}, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), allowGoodbye: &atomic.Bool{}}
	bufs, sizes := [][]byte{make([]byte, 128)}, []int{0}
	if allocations := testing.AllocsPerRun(100, func() { filter.Read(bufs, sizes, 16) }); allocations != 0 {
		t.Fatalf("normal Read allocated %v times", allocations)
	}
}
