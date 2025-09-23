package protocols

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwricu/bad-proxy-core/structure"
)

const (
	btpDigestLen       = 32
	btpConfusionLenDig = 1
	btpTimestampLen    = 4
	btpDirectiveDig    = 1
	btpHostLenDig      = 1
	btpPortLen         = 2
	btpHeaderLen       = btpDigestLen +
		btpConfusionLenDig +
		btpTimestampLen +
		btpHostLenDig +
		btpDirectiveDig +
		btpPortLen
	timeThreshold      = 210
	btpMaxConfusionLen = 64
	btpTimeDiffRand    = 30
)

type BTPRequest struct {
	Address      string
	Payload      []byte
	digest       string
	confusionLen int
	timeDiff     int
	headerLen    int
	rawData      []byte
}

var btpLRU *structure.LRU[string]
var once sync.Once

func GetBtpCache() *structure.LRU[string] {
	once.Do(func() {
		instance := &structure.LRU[string]{}
		instance.Init(210000)
		btpLRU = instance
	})
	return btpLRU
}

func (request *BTPRequest) validate(secret string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println(r)
		}
	}()
	if request.confusionLen < 0 || request.confusionLen > btpMaxConfusionLen {
		return errors.New("unexpected confusion length")
	}
	if request.timeDiff < -timeThreshold || timeThreshold < request.timeDiff {
		return errors.New("timeout or replay attack")
	}

	h := hmac.New(sha256.New, []byte(secret))
	h.Write(request.rawData[btpDigestLen:])
	if hex.EncodeToString(h.Sum(nil)) != request.digest {
		return errors.New("digest mismatch, unauthorized connect")
	}

	if err = GetBtpCache().Add(request.digest); err != nil {
		return
	}

	if request.headerLen != btpHeaderLen {
		return errors.New("wrong btp head len")
	}

	return
}

func parseBtpRequest(rawData []byte) (request *BTPRequest, err error) {
	defer func() {
		if r := recover(); r != nil {
			request.Payload = rawData // restore raw data
			log.Println(r)
		}
	}()
	request = &BTPRequest{}
	request.digest = hex.EncodeToString(rawData[:btpDigestLen])
	request.confusionLen = int(rawData[btpDigestLen]) // uint8

	pos := btpDigestLen + btpConfusionLenDig + request.confusionLen
	timestamp := binary.BigEndian.Uint32(rawData[pos : pos+btpTimestampLen])
	request.timeDiff = int(time.Now().Unix()) - int(timestamp)

	pos += btpTimestampLen + btpDirectiveDig
	hostLen := int(rawData[pos])
	pos += btpHostLenDig
	host := string(rawData[pos : pos+hostLen])
	pos += hostLen // possible out of bound
	port := strconv.Itoa(int(binary.BigEndian.Uint16(rawData[pos : pos+btpPortLen])))
	pos += btpPortLen

	request.headerLen = pos - request.confusionLen - hostLen
	request.Address = host + ":" + port
	request.Payload = rawData[pos:]
	request.rawData = rawData
	return request, nil
}

func encodeBtpRequest(address string, payload []byte, secret string) (res []byte, err error) {
	bytesBuffer := bytes.NewBuffer([]byte{})
	confusionLen, err := rand.Int(rand.Reader, big.NewInt(btpMaxConfusionLen))
	if err != nil {
		return
	}
	confusion := make([]byte, int(confusionLen.Int64()))
	n, err := rand.Read(confusion)
	if n != int(confusionLen.Int64()) || err != nil {
		return
	}

	timeDiff, err := rand.Int(rand.Reader, big.NewInt(2*btpTimeDiffRand))
	if err != nil {
		return
	}
	timestamp := time.Now().Unix() + timeDiff.Int64() - btpTimeDiffRand // time +-30

	hnp := strings.Split(address, ":")
	host := []byte(hnp[0])
	port, err := strconv.Atoi(hnp[1])
	if err != nil || port > int(^uint16(0)) {
		return
	}

	_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(confusionLen.Uint64()))
	_ = binary.Write(bytesBuffer, binary.BigEndian, confusion)
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint32(timestamp))
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(1)) // directive
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint8(len(host)))
	_ = binary.Write(bytesBuffer, binary.BigEndian, host)
	_ = binary.Write(bytesBuffer, binary.BigEndian, uint16(port))
	_ = binary.Write(bytesBuffer, binary.BigEndian, payload)

	body := bytesBuffer.Bytes()

	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	digest := h.Sum(nil)
	res = append(digest, body...)
	return
}

type BtpInbound struct {
	Conn   net.Conn
	Secret string
}

func (inbound *BtpInbound) Fallback(rawData []byte) {
	if _, err := parseHttpRequest(rawData); err != nil {
		return
	}
	page := []byte("<html><body><a href=\"https://wwr.icu\">Please login<a>\r\n</body></html>")
	header := []byte("HTTP/1.1 200 OK\r\nContent-Type:text/html\r\nContent-Length:" + strconv.Itoa(len(page)) + "Connection: close \r\n\r\n")
	_, _ = inbound.Write(append(header, page...))
}

func (inbound *BtpInbound) Connect() (targetAddr string, payload []byte, err error) {
	payload = make([]byte, 8196) // return rawData on error
	length, err := inbound.Conn.Read(payload)
	if err != nil {
		return
	}
	request, err := parseBtpRequest(payload[:length])
	if err != nil {
		return
	}
	if err = request.validate(inbound.Secret); err != nil { // try to handle http connection
		return
	}
	return request.Address, request.Payload, nil
}

func (inbound *BtpInbound) Read(b []byte) (int, error) {
	return inbound.Conn.Read(b)
}

func (inbound *BtpInbound) Write(b []byte) (int, error) {
	return inbound.Conn.Write(b)
}

func (inbound *BtpInbound) Close() error {
	return inbound.Conn.Close()
}

type BtpOutbound struct {
	Conn   net.Conn
	Secret string
}

func (outbound *BtpOutbound) Connect(targetAddr string, payload []byte) (err error) {
	if payload, err = encodeBtpRequest(targetAddr, payload, outbound.Secret); err != nil {
		return
	}
	_, err = outbound.Conn.Write(payload)
	return
}

func (outbound *BtpOutbound) Read(b []byte) (int, error) {
	return outbound.Conn.Read(b)
}

func (outbound *BtpOutbound) Write(b []byte) (int, error) {
	return outbound.Conn.Write(b)
}

func (outbound *BtpOutbound) Close() error {
	return outbound.Conn.Close()
}
