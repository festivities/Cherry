package lpn

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"net/http"
	"strconv"

	"cherry/internal/httpx"
)

const SckeyEntryCount = 100

var (
	SckeyWrapperKey = []byte("F609C5FCEFE3F62C")
	SckeyWrapperIV  = []byte("B0F9926983812A17")
	sckeyKey64      = []byte("0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF")
	sckeyIV16       = []byte("FEDCBA9876543210")
	SckeyBlob       = buildSckeyBlob()
)

func buildSckeyBlob() []byte {
	entry := make([]byte, 0, 88)
	entry = binary.BigEndian.AppendUint32(entry, uint32(len(sckeyKey64)))
	entry = append(entry, WrapSckeyField(sckeyKey64)...)
	entry = binary.BigEndian.AppendUint32(entry, uint32(len(sckeyIV16)))
	entry = append(entry, WrapSckeyField(sckeyIV16)...)

	blob := make([]byte, 0, 12+SckeyEntryCount*len(entry))
	blob = binary.BigEndian.AppendUint32(blob, 0)
	blob = binary.BigEndian.AppendUint64(blob, 0)
	for i := 0; i < SckeyEntryCount; i++ {
		blob = append(blob, entry...)
	}
	return blob
}

func WrapSckeyField(plain []byte) []byte {
	block, _ := aes.NewCipher(SckeyWrapperKey)
	out := make([]byte, len(plain))
	cipher.NewCFBEncrypter(block, SckeyWrapperIV).XORKeyStream(out, plain)
	return out
}

func HandleSckeyEnc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(SckeyBlob)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(SckeyBlob)
}
