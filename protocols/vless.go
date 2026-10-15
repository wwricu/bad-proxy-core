package protocols

import (
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/wwricu/bad-proxy-core/common"
)

/*
VLESS request, byte-for-byte compatible with current Xray
(proxy/vless/encoding.EncodeRequestHeader, verified against Xray 26.3.27):

	+-------+------+------+------------+------+----------+-------+----------+
	| VERSION | ID | ADDON | ADDON BYTES | CMD | DST.PORT | ATYP  | DST.ADDR |
	+-------+------+------+------------+------+----------+-------+----------+
	|   1(=0) |  16 |   1   |   var(0)   |  1   |    2     |   1   | Variable |

Note the two traps versus the old v2fly documentation: VERSION is 0, and the
port precedes the address (Xray's addrParser uses PortThenAddress). Nonzero
ADDON BYTES only occur with xtls-rprx-vision protobuf addons, which this build
rejects. Response is just VERSION(0) + addon length byte(0); Xray dropped the
old status byte - a failed handshake simply closes.

VLESS carries no encryption of its own: the UUID travels in clear and the
stream security is delegated to the transport (TLS terminated by nginx, or an
outer tls transport). Only CMD=1 (TCP) is supported.
*/

const (
	VlessVersion = 0
	CmdTCP       = 1
	CmdUDP       = 2
	CmdMux       = 3
)

// ErrNoStreamUpgrade signals an xtls-rprx-vision flow, which needs an inner
// TLS stream this build does not implement.
var ErrNoStreamUpgrade = errors.New("vless: unsupported flow")

type VlessUser [16]byte

func ParseID(id string) (VlessUser, error) {
	clean := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "-", "")
	p, err := hex.DecodeString(clean)
	if err != nil {
		return VlessUser{}, err
	}
	if len(p) != 16 {
		return VlessUser{}, errors.New("vless: id must be a 16 byte uuid")
	}
	return VlessUser(p), nil
}

func (id VlessUser) String() string {
	s := hex.EncodeToString(id[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

// addressWriter lets EncodeTarget emit straight into a request buffer instead
// of a socket; shared by the VLESS, Trojan and SOCKS outbounds.
type addressWriter struct {
	w *[]byte
}

func (a addressWriter) Write(p []byte) (int, error) {
	*a.w = append(*a.w, p...)
	return len(p), nil
}

// VlessInbound is the server side: it decodes the request header, authorizes
// the UUID, and answers with the deferred two-byte response.
type VlessInbound struct {
	*common.Buffered
	users map[VlessUser]bool
	// probe keeps the first bytes before the handshake consumes them; the
	// fallback page must recognize an HTTP GET that failed version parsing.
	probe []byte
}

func NewVlessInbound(conn net.Conn, ids []string) (*VlessInbound, error) {
	users := make(map[VlessUser]bool, len(ids))
	for _, raw := range ids {
		id, err := ParseID(raw)
		if err != nil {
			return nil, err
		}
		users[id] = true
	}
	if len(users) == 0 {
		return nil, errors.New("vless inbound needs at least one user id")
	}
	return &VlessInbound{Buffered: common.NewBuffered(conn), users: users}, nil
}

func (in *VlessInbound) Connect() (targetAddr string, err error) {
	in.probe = in.Peek(8)
	version, err := in.ReadByte()
	if err != nil {
		return
	}
	if version != VlessVersion {
		return "", errors.New("vless: unknown version")
	}
	raw, err := in.ReadFull(16)
	if err != nil {
		return
	}
	id := VlessUser(raw)
	if !in.users[id] {
		return "", errors.New("vless: unauthorized id " + id.String())
	}
	addonLen, err := in.ReadByte()
	if err != nil {
		return
	}
	if addonLen > 0 {
		// only xtls-rprx-vision writes non-empty addons; skip them and refuse
		if _, err = in.ReadFull(int(addonLen)); err != nil {
			return
		}
		return "", ErrNoStreamUpgrade
	}
	command, err := in.ReadByte()
	if err != nil {
		return
	}
	if command != CmdTCP {
		return "", errors.New("vless: only tcp is supported")
	}
	if targetAddr, err = in.DecodeTarget(common.TargetCodingVless); err != nil {
		return
	}
	if _, err = in.Write([]byte{VlessVersion, 0}); err != nil {
		return
	}
	return targetAddr, nil
}

func (in *VlessInbound) Fallback() {
	if common.LooksLikeHttpRequest(in.probe) {
		common.ServeFallbackPage(in.Buffered)
	}
}

// VlessOutbound is the client side: it encodes the request header and, because
// Xray only flushes the two-byte response together with the first downstream
// data, strips that response lazily on the first read.
type VlessOutbound struct {
	*common.Buffered
	id VlessUser
	// noSkipResp guards the lazy two-byte response skip below
	noSkipResp bool
}

func NewVlessOutbound(conn net.Conn, id string) (*VlessOutbound, error) {
	parsed, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	return &VlessOutbound{Buffered: common.NewBuffered(conn), id: parsed}, nil
}

func (out *VlessOutbound) Connect(targetAddr string) error {
	header := make([]byte, 0, 32)
	header = append(header, VlessVersion)
	header = append(header, out.id[:]...)
	header = append(header, 0) // addons: empty
	header = append(header, CmdTCP)
	writer := addressWriter{w: &header}
	if err := common.EncodeTarget(writer, targetAddr, common.TargetCodingVless); err != nil {
		return err
	}
	_, err := out.Write(header)
	if err == nil {
		out.noSkipResp = true
	}
	return err
}

// skipResp strips the deferred VLESS response header from the first
// downstream bytes of the tunnel.
func (out *VlessOutbound) skipResp() error {
	if !out.noSkipResp {
		return nil
	}
	var resp [2]byte
	if _, err := io.ReadFull(out.Buffered, resp[:]); err != nil {
		return err
	}
	if resp[0] != VlessVersion {
		return errors.New("vless: unexpected response version")
	}
	if resp[1] != 0 {
		return ErrNoStreamUpgrade // server announced protobuf addons (vision)
	}
	out.noSkipResp = false
	return nil
}

func (out *VlessOutbound) Read(p []byte) (int, error) {
	if err := out.skipResp(); err != nil {
		return 0, err
	}
	return out.Buffered.Read(p)
}

// WriteTo must be redefined: the promoted Buffered.WriteTo would let
// io.Copy splice straight from the socket and leak the response header.
func (out *VlessOutbound) WriteTo(w io.Writer) (int64, error) {
	if err := out.skipResp(); err != nil {
		return 0, err
	}
	return out.Buffered.WriteTo(w)
}
