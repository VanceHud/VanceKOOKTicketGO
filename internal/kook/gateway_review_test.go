package kook

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"testing"
)

func TestGatewayRejectsOversizedDecompressedFrames(t *testing.T) {
	for _, raw := range []bool{false, true} {
		var payload bytes.Buffer
		if raw {
			writer, err := flate.NewWriter(&payload, flate.BestSpeed)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = writer.Write(bytes.Repeat([]byte(" "), maxGatewayFrameBytes+1))
			_ = writer.Close()
		} else {
			writer := zlib.NewWriter(&payload)
			_, _ = writer.Write(bytes.Repeat([]byte(" "), maxGatewayFrameBytes+1))
			_ = writer.Close()
		}
		if _, err := decompress(payload.Bytes()); err == nil {
			t.Fatalf("超大压缩帧被接受 raw=%v", raw)
		}
	}
}
