package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"
)

var certDNSNames = []string{
	"fapi.play.naver.jp",
	"fapi-diary.play.naver.jp",
	"session.play.naver.jp",
	"gws.play.naver.jp",
	"event.play.naver.jp",
	"face.play.naver.jp",
	"terms.play.naver.jp",
	"ads.play.naver.jp",
	"play-static.line-scdn.net",
	"media-pu.line-scdn.net",
	"obs.line-scdn.net",
	"play-static.line-apps.com",
	"obs.line-apps.com",
	"channel-apis.line.naver.jp",
	"api.line.naver.jp",
	"tx.lbg.play.naver.jp",
	"gws.play.naver.net",
	"fapi.play.naver.net",
}

func generateCerts() ([]tls.Certificate, error) {
	now := time.Now()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	rsaCert, err := selfSign(&rsaKey.PublicKey, rsaKey, now)
	if err != nil {
		return nil, err
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ecCert, err := selfSign(&ecKey.PublicKey, ecKey, now)
	if err != nil {
		return nil, err
	}
	return []tls.Certificate{rsaCert, ecCert}, nil
}

func selfSign(pub, priv any, now time.Time) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cherry"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              certDNSNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}
