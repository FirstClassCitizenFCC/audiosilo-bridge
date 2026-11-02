package audible

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// voucherKeyIV derives the AES-128-CBC key/iv that unwrap a book's license
// voucher: SHA256(device_type + device_serial + customer_id + asin), first 16
// bytes = key, next 16 = iv. (The device RSA key only signs the license request;
// the voucher unwrap is derived, not RSA.)
func voucherKeyIV(deviceType, serial, customerID, asin string) (key, iv []byte) {
	sum := sha256.Sum256([]byte(deviceType + serial + customerID + asin))
	return sum[:16], sum[16:32]
}

// voucherKeys is the per-book decryption key/iv (hex) handed to ffmpeg.
type voucherKeys struct {
	Key string `json:"key"`
	IV  string `json:"iv"`
}

// DecryptVoucher unwraps a base64 license voucher (AES-128-CBC, no padding) into
// the per-book ffmpeg key/iv. The decrypted payload is JSON containing {key, iv};
// CBC leaves trailing bytes, so the embedded JSON object is extracted.
func DecryptVoucher(b64Voucher, deviceType, serial, customerID, asin string) (key, iv string, err error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64Voucher))
	if err != nil {
		return "", "", fmt.Errorf("voucher base64: %w", err)
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return "", "", fmt.Errorf("voucher length %d is not a multiple of %d", len(data), aes.BlockSize)
	}
	k, ivBytes := voucherKeyIV(deviceType, serial, customerID, asin)
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", "", err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, ivBytes).CryptBlocks(out, data)

	start := bytes.IndexByte(out, '{')
	end := bytes.LastIndexByte(out, '}')
	if start < 0 || end < start {
		return "", "", fmt.Errorf("voucher: no JSON object in the decrypted payload")
	}
	var vk voucherKeys
	if err := json.Unmarshal(out[start:end+1], &vk); err != nil {
		return "", "", fmt.Errorf("voucher json: %w", err)
	}
	if vk.Key == "" || vk.IV == "" {
		return "", "", fmt.Errorf("voucher missing key/iv")
	}
	return vk.Key, vk.IV, nil
}
