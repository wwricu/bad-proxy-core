package protocols

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/structure"
)

/*
Bad-Proxy-Transport v2.

+----------+------------+-----------+-----------+-----+-----+-----+------+-----+
| HMAC     | confusion  | confusion | timestamp | dir | len | host| port | ... |
| digest   | length     | bytes     |           |     |     |     | (2B) |     |
+----------+------------+-----------+-----------+-----+-----+-----+------+-----+
|    32    |     1      |    0-64   |     4     |  1  |  1  | Var |  2   |     |

The HMAC-SHA256 over the header authenticates the request and, together with
the timestamp window plus the digest replay cache, stops replays. The body
after the header is the raw relayed stream: integrity of payload bytes is
delegated to the transport (TLS), as in VLESS/Trojan.

v1 computed the digest over "header + first payload segment", which required
the whole segment to arrive in a single TCP read and made the receiver's
buffer length part of the authentication input - fragmented connections
failed intermittently. v2 binds the header only and streams cleanly.
*/

const (
	btpDigestLen    = 32
	btpTimestampLen = 4
	btpDirective    = 1
	timeThreshold   = 210
	maxConfusion    = 64
	maxBtpHostLen   = 255
	timeDiffRand    = 30
)

var (
	btpLRU     *structure.LRU[string]
	btpLRUOnce sync.Once
)

func GetBtpCache() *structure.LRU[string] {
	btpLRUOnce.Do(func() {
		instance := &structure.LRU[string]{}
		instance.Init(210000)
		btpLRU = instance
	})
	return btpLRU
}

// btpHeader is the parsed, not yet authenticated, request head.
type btpHeader struct {
	digest       []byte
	confusion    []byte
	timestamp    uint32
	host         string
	port         uint16
	signedRegion []byte // everything after the digest, input of the HMAC
}

func (h *btpHeader) validate(secret string) error {
	if len(h.confusion) > maxConfusion {
		return errors.New("unexpected confusion length")
	}
	diff := int(time.Now().Unix()) - int(h.timestamp)
	if diff < -timeThreshold || timeThreshold < diff {
		return errors.New("timeout or replay attack")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(h.signedRegion)
	if !hmac.Equal(mac.Sum(nil), h.digest) {
		return errors.New("digest mismatch, unauthorized connect")
	}
	return GetBtpCache().Add(hex.EncodeToString(h.digest))
}

func readBtpHeader(in *Buffered) (*btpHeader, error) {
	header := &btpHeader{}
	digest, err := in.ReadFull(btpDigestLen)
	if err != nil {
		return nil, err
	}
	header.digest = digest

	confusionLen, err := in.ReadByte()
	if err != nil {
		return nil, err
	}
	if confusionLen > maxConfusion {
		return nil, errors.New("bad confusion length")
	}
	signed := make([]byte, 0, 96)
	signed = append(signed, confusionLen)
	header.signedRegion = signed

	if confusionLen > 0 {
		confusion, err := in.ReadFull(int(confusionLen))
		if err != nil {
			return nil, err
		}
		header.confusion = confusion
		header.signedRegion = append(header.signedRegion, confusion...)
	}

	stamp, err := in.ReadFull(btpTimestampLen + 1) // timestamp + directive
	if err != nil {
		return nil, err
	}
	header.timestamp = binary.BigEndian.Uint32(stamp[:btpTimestampLen])
	header.signedRegion = append(header.signedRegion, stamp...)

	hostLen, err := in.ReadByte()
	if err != nil {
		return nil, err
	}
	if hostLen == 0 || hostLen > maxBtpHostLen {
		return nil, errors.New("bad host length")
	}
	host, err := in.ReadFull(int(hostLen))
	if err != nil {
		return nil, err
	}
	header.host = string(host)
	header.signedRegion = append(header.signedRegion, hostLen)
	header.signedRegion = append(header.signedRegion, host...)

	port, err := in.ReadUint16()
	if err != nil {
		return nil, err
	}
	header.port = port
	header.signedRegion = append(header.signedRegion, byte(port>>8), byte(port))
	return header, nil
}

// encodeBtpHeader builds the digest-prefixed header; the caller writes the
// relayed payload afterwards.
func encodeBtpHeader(address string, secret string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}
	if len(host) == 0 || len(host) > maxBtpHostLen {
		return nil, errors.New("bad host length")
	}

	confusionLen, err := rand.Int(rand.Reader, big.NewInt(maxConfusion+1))
	if err != nil {
		return nil, err
	}
	n := int(confusionLen.Int64())
	confusion := make([]byte, n)
	if _, err = io.ReadFull(rand.Reader, confusion); err != nil {
		return nil, err
	}
	offset, err := rand.Int(rand.Reader, big.NewInt(2*timeDiffRand))
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().Unix() + offset.Int64() - timeDiffRand

	header := make([]byte, 0, 1+n+5+1+len(host)+2)
	header = append(header, byte(n))
	header = append(header, confusion...)
	stamp := make([]byte, 4)
	binary.BigEndian.PutUint32(stamp, uint32(timestamp))
	header = append(header, stamp...)
	header = append(header, btpDirective, byte(len(host)))
	header = append(header, host...)
	header = append(header, byte(port>>8), byte(port))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(header)
	return append(mac.Sum(nil), header...), nil
}

type BtpInbound struct {
	*Buffered
	secrets []string
	// probe keeps the first bytes before the handshake consumes them; the
	// fallback page must recognize an HTTP GET that failed header parsing.
	probe []byte
}

func NewBtpInbound(conn net.Conn, secrets []string) *BtpInbound {
	return &BtpInbound{Buffered: NewBuffered(conn), secrets: secrets}
}

func (in *BtpInbound) Connect() (targetAddr string, err error) {
	in.probe = in.Peek(8)
	if LooksLikeHttpRequest(in.probe) {
		// a web probe can never be a BTP handshake; fail now so Fallback()
		// answers while the client is still connected
		return "", errors.New("http probe")
	}
	header, err := readBtpHeader(in.Buffered)
	if err != nil {
		return "", err
	}
	for _, secret := range in.secrets {
		if err = header.validate(secret); err == nil {
			return net.JoinHostPort(header.host, strconv.Itoa(int(header.port))), nil
		}
	}
	return "", err
}

func (in *BtpInbound) Fallback() {
	if LooksLikeHttpRequest(in.probe) {
		ServeFallbackPage(in.Buffered)
		return
	}
	log.Println("btp inbound: unauthorized probe")
}

type BtpOutbound struct {
	*Buffered
	secret string
}

func NewBtpOutbound(conn net.Conn, secret string) *BtpOutbound {
	return &BtpOutbound{Buffered: NewBuffered(conn), secret: secret}
}

func (out *BtpOutbound) Connect(targetAddr string) error {
	header, err := encodeBtpHeader(targetAddr, out.secret)
	if err != nil {
		return err
	}
	_, err = out.Write(header)
	return err
}
