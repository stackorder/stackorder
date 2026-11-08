package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestS3Endpoint(t *testing.T) {
	tests := map[string]string{
		"":     "",
		"  \n": "",
		"https://0123abcd.r2.cloudflarestorage.com":    "https://0123abcd.r2.cloudflarestorage.com",
		" HTTPS://0123ABCD.R2.cloudflarestorage.com/ ": "https://0123abcd.r2.cloudflarestorage.com",
		"0123abcd.eu.r2.cloudflarestorage.com":         "https://0123abcd.eu.r2.cloudflarestorage.com",
		"https://minio.example.com:443/":               "https://minio.example.com",
		"http://minio.internal:80":                     "http://minio.internal",
		"http://minio.internal:9000":                   "http://minio.internal:9000",
		"https://minio.internal:9000/s3/":              "https://minio.internal:9000/s3",
		"https://user:pass@minio.internal/?x=1#f":      "https://minio.internal",
		"http://[::1]:80":                              "http://[::1]",
		"http://[::1]:4566":                            "http://[::1]:4566",
		"https://s3.us-east-1.amazonaws.com":           "https://s3.us-east-1.amazonaws.com",
		"https://%zz/":                                 "https://%zz",
	}
	for in, want := range tests {
		assert.Equal(t, want, S3Endpoint(in), "S3Endpoint(%q)", in)
	}
}

func TestS3Service(t *testing.T) {
	tests := map[string]string{
		"":                                     "",
		"https://s3.amazonaws.com":             AWSS3,
		"https://s3.eu-west-1.amazonaws.com/":  AWSS3,
		"s3.dualstack.us-east-1.amazonaws.com": AWSS3,
		"https://s3-fips.us-gov-west-1.amazonaws.com":                       AWSS3,
		"https://s3.cn-north-1.amazonaws.com.cn":                            AWSS3,
		"https://bucket.vpce-1a2b3c4d-5e6f.s3.us-east-1.vpce.amazonaws.com": AWSS3,
		"https://s3.us-east-1.api.aws":                                      AWSS3,
		"https://0123abcd.r2.cloudflarestorage.com/":                        "https://0123abcd.r2.cloudflarestorage.com",
		"https://amazonaws.com.example.com":                                 "https://amazonaws.com.example.com",
		"https://notamazonaws.com":                                          "https://notamazonaws.com",
	}
	for in, want := range tests {
		assert.Equal(t, want, S3Service(in), "S3Service(%q)", in)
	}
}
