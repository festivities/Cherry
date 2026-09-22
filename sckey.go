package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"net/http"
	"strconv"
)

const sckeyEntryCount = 100

var (
	sckeyWrapperKey = []byte("F609C5FCEFE3F62C")
	sckeyWrapperIV  = []byte("B0F9926983812A17")
	sckeyKey64      = []byte("0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF")
	sckeyIV16       = []byte("FEDCBA9876543210")
	sckeyBlob       = buildSckeyBlob()
)

func buildSckeyBlob() []byte {
	entry := make([]byte, 0, 88)
	entry = binary.BigEndian.AppendUint32(entry, uint32(len(sckeyKey64)))
	entry = append(entry, wrapSckeyField(sckeyKey64)...)
	entry = binary.BigEndian.AppendUint32(entry, uint32(len(sckeyIV16)))
	entry = append(entry, wrapSckeyField(sckeyIV16)...)

	blob := make([]byte, 0, 12+sckeyEntryCount*len(entry))
	blob = binary.BigEndian.AppendUint32(blob, 0)
	blob = binary.BigEndian.AppendUint64(blob, 0)
	for i := 0; i < sckeyEntryCount; i++ {
		blob = append(blob, entry...)
	}
	return blob
}

func wrapSckeyField(plain []byte) []byte {
	block, _ := aes.NewCipher(sckeyWrapperKey)
	out := make([]byte, len(plain))
	cipher.NewCFBEncrypter(block, sckeyWrapperIV).XORKeyStream(out, plain)
	return out
}

func handleSckeyEnc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(sckeyBlob)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sckeyBlob)
}
