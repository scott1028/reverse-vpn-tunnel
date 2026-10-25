package tunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"
	"reverse-vpn-tunnel/internal/config"
)

func getGoodbyePacket() []byte {
	packet := make([]byte, 28+len(goodbyeToken))
	packet[0], packet[9] = 0x45, 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], []byte{10, 77, 0, 2})
	copy(packet[16:20], []byte{10, 77, 0, 1})
	binary.BigEndian.PutUint16(packet[22:24], config.ControlPort)
	binary.BigEndian.PutUint16(packet[24:26], uint16(len(packet)-20))
	copy(packet[28:], goodbyeToken)
	return packet
}

func TestGoodbyePacketBeforeKernel(t *testing.T) {
	if !acceptGoodbye(getGoodbyePacket(), netip.MustParseAddr("10.77.0.1"), netip.MustParseAddr("10.77.0.2")) {
		t.Fatal("decrypted IPv4 goodbye packet was not recognized")
	}
}

type recordingTUN struct {
	tun.Device
	packets [][]byte
	offset  int
	err     error
}

func (t *recordingTUN) Write(bufs [][]byte, offset int) (int, error) {
	t.offset = offset
	if t.err != nil {
		return 0, t.err
	}
	for _, buf := range bufs {
		t.packets = append(t.packets, bytes.Clone(buf))
	}
	return len(bufs), nil
}

func TestAcceptGoodbye(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func([]byte) []byte
		want  bool
	}{
		{"valid", func(p []byte) []byte { return p }, true},
		{"DF flag", func(p []byte) []byte { p[6] = 0x40; return p }, true},
		{"wrong source", func(p []byte) []byte { p[12] = 192; return p }, false},
		{"entry as source", func(p []byte) []byte { p[15] = 1; return p }, false},
		{"wrong destination", func(p []byte) []byte { p[19] = 3; return p }, false},
		{"wrong port", func(p []byte) []byte { p[23]++; return p }, false},
		{"wrong payload", func(p []byte) []byte { p[len(p)-1] = '2'; return p }, false},
		{"trailing byte", func(p []byte) []byte {
			p = append(p, 0)
			binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
			binary.BigEndian.PutUint16(p[24:26], uint16(len(p)-20))
			return p
		}, false},
		{"trailing newline", func(p []byte) []byte {
			p = append(p, '\n')
			binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
			binary.BigEndian.PutUint16(p[24:26], uint16(len(p)-20))
			return p
		}, false},
		{"empty payload", func(p []byte) []byte { return p[:28] }, false},
		{"TCP", func(p []byte) []byte { p[9] = 6; return p }, false},
		{"ICMP", func(p []byte) []byte { p[9] = 1; return p }, false},
		{"IPv6", func(p []byte) []byte {
			v6 := make([]byte, 40+len(p)-20)
			v6[0], v6[6] = 0x60, 17
			binary.BigEndian.PutUint16(v6[4:6], uint16(len(v6)-40))
			copy(v6[40:], p[20:])
			return v6
		}, false},
		{"empty", func(p []byte) []byte { return p[:0] }, false},
		{"short IPv4", func(p []byte) []byte { return p[:19] }, false},
		{"short UDP", func(p []byte) []byte { return p[:27] }, false},
		{"IPv4 options", func(p []byte) []byte {
			options := append(bytes.Clone(p[:20]), 0, 0, 0, 0)
			options = append(options, p[20:]...)
			options[0] = 0x46
			binary.BigEndian.PutUint16(options[2:4], uint16(len(options)))
			return options
		}, false},
		{"short IHL", func(p []byte) []byte { p[0] = 0x44; return p }, false},
		{"more fragments", func(p []byte) []byte { p[6] = 0x20; return p }, false},
		{"fragment offset", func(p []byte) []byte { p[7] = 1; return p }, false},
		{"reserved flag", func(p []byte) []byte { p[6] = 0x80; return p }, false},
		{"bad IP length", func(p []byte) []byte { p[3]++; return p }, false},
		{"bad UDP length", func(p []byte) []byte { p[25]++; return p }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			packet := test.alter(getGoodbyePacket())
			device := &recordingTUN{}
			goodbyes := make(chan time.Time, 1)
			filter := &goodbyeFilter{Device: device, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), goodbyes: goodbyes}
			buf := append(bytes.Repeat([]byte{0xa5}, 16), packet...)
			before := time.Now()
			n, err := filter.Write([][]byte{buf}, 16)
			if err != nil || n != 1 {
				t.Fatalf("Write = %d, %v", n, err)
			}
			if test.want {
				select {
				case received := <-goodbyes:
					if received.Before(before) || received.After(time.Now()) {
						t.Fatal("goodbye timestamp is not receive time")
					}
				default:
					t.Fatal("goodbye was not signaled")
				}
				if len(device.packets) != 0 {
					t.Fatal("goodbye reached the OS")
				}
			} else if len(goodbyes) != 0 || device.offset != 16 || !reflect.DeepEqual(device.packets, [][]byte{buf}) {
				t.Fatal("non-goodbye packet did not pass through unchanged")
			}
		})
	}
}

func TestGoodbyeFilterPreservesBatchOrder(t *testing.T) {
	first, second := getGoodbyePacket(), getGoodbyePacket()
	first[9], second[9] = 6, 1
	device := &recordingTUN{}
	goodbyes := make(chan time.Time, 2)
	filter := &goodbyeFilter{Device: device, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), goodbyes: goodbyes}
	if n, err := filter.Write([][]byte{getGoodbyePacket(), first, getGoodbyePacket(), second}, 0); err != nil || n != 4 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(goodbyes) != 2 || !reflect.DeepEqual(device.packets, [][]byte{first, second}) {
		t.Fatal("mixed batch was not filtered in order")
	}
}

func TestGoodbyeFilterFullChannelDoesNotBlock(t *testing.T) {
	goodbyes := make(chan time.Time, 1)
	goodbyes <- time.Now()
	device := &recordingTUN{}
	filter := &goodbyeFilter{Device: device, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), goodbyes: goodbyes}
	done := make(chan int, 1)
	go func() {
		n, err := filter.Write([][]byte{getGoodbyePacket()}, 0)
		if err != nil {
			t.Error(err)
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != 1 || len(device.packets) != 0 {
			t.Fatal("full channel stopped filtering")
		}
	case <-time.After(time.Second):
		t.Fatal("Write blocked on a full channel")
	}
}

func TestGoodbyeFilterPreservesWriteError(t *testing.T) {
	writeErr := errors.New("TUN write failed")
	device := &recordingTUN{err: writeErr}
	goodbyes := make(chan time.Time, 1)
	filter := &goodbyeFilter{Device: device, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2"), goodbyes: goodbyes}
	normal := getGoodbyePacket()
	normal[9] = 6
	if n, err := filter.Write([][]byte{normal, getGoodbyePacket()}, 0); n != 1 || err != writeErr || len(goodbyes) != 1 {
		t.Fatalf("Write = %d, %v; pending goodbyes = %d", n, err, len(goodbyes))
	}
}

type discardTUN struct{ tun.Device }

// Match native Linux's byte count rather than tun.Device's packet-count contract.
func (*discardTUN) Write(bufs [][]byte, offset int) (int, error) {
	count := 0
	for _, buf := range bufs {
		count += len(buf) - offset
	}
	return count, nil
}

func TestGoodbyeFilterNormalizesPacketCount(t *testing.T) {
	packet := getGoodbyePacket()
	packet[9] = 6
	filter := &goodbyeFilter{Device: &discardTUN{}}
	if n, err := filter.Write([][]byte{packet, packet}, 0); n != 2 || err != nil {
		t.Fatalf("Write = %d, %v; want two consumed packets", n, err)
	}
}

func TestGoodbyeFilterShortBufferBeforeOffset(t *testing.T) {
	device := &recordingTUN{}
	filter := &goodbyeFilter{Device: device}
	buf := []byte{1, 2}
	if n, err := filter.Write([][]byte{buf}, 16); n != 1 || err != nil || !reflect.DeepEqual(device.packets, [][]byte{buf}) {
		t.Fatalf("short buffer did not pass through: %d, %v", n, err)
	}
}

func TestGoodbyeFilterDoesNotAllocateForNormalTraffic(t *testing.T) {
	packet := getGoodbyePacket()
	packet[len(packet)-1] = '2'
	bufs := [][]byte{packet}
	filter := &goodbyeFilter{Device: &discardTUN{}, local: netip.MustParseAddr("10.77.0.1"), peer: netip.MustParseAddr("10.77.0.2")}
	if allocations := testing.AllocsPerRun(100, func() { filter.Write(bufs, 0) }); allocations != 0 {
		t.Fatalf("normal Write allocated %v times", allocations)
	}
}
