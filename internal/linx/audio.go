package linx

import (
	"bytes"
	"encoding/binary"
)

// PCMToWAV wraps mono PCM16LE sampled at 16kHz for native browser playback.
func PCMToWAV(pcm []byte) []byte {
	return PCMToWAVAtRate(pcm, 16000)
}

// PCMToWAVAtRate preserves the actual sample rate negotiated with Linx.
func PCMToWAVAtRate(pcm []byte, sampleRate int) []byte {
	b := make([]byte, 44+len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(b[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	copy(b[44:], pcm)
	return b
}

// OpusOgg packages raw Opus WebSocket frames; Ogg granules always use 48kHz.
func OpusOgg(packets [][]byte, sampleRate int) []byte {
	var out bytes.Buffer
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 1
	binary.LittleEndian.PutUint32(head[12:], uint32(sampleRate))
	out.Write(oggPage(head, 2, 0, 0))
	tags := make([]byte, 8+4+len("undercover")+4)
	copy(tags, "OpusTags")
	binary.LittleEndian.PutUint32(tags[8:], uint32(len("undercover")))
	copy(tags[12:], "undercover")
	out.Write(oggPage(tags, 0, 0, 1))
	granule := uint64(0)
	for i, p := range packets {
		granule += uint64(opusSamples(p))
		flags := byte(0)
		if i == len(packets)-1 {
			flags = 4
		}
		out.Write(oggPage(p, flags, granule, uint32(i+2)))
	}
	return out.Bytes()
}
func opusSamples(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	config := p[0] >> 3
	var samples int
	if config >= 16 {
		samples = 120 << uint(config&3)
	} else if config >= 12 {
		samples = 480 << uint(config&1)
	} else {
		samples = []int{480, 960, 1920, 2880}[config&3]
	}
	count := 1
	switch p[0] & 3 {
	case 1, 2:
		count = 2
	case 3:
		if len(p) < 2 {
			return 0
		}
		count = int(p[1] & 63)
	}
	if samples*count > 5760 {
		return 0
	}
	return samples * count
}
func oggPage(data []byte, flags byte, granule uint64, sequence uint32) []byte {
	segments := len(data)/255 + 1
	b := make([]byte, 27+segments+len(data))
	copy(b, "OggS")
	b[5] = flags
	binary.LittleEndian.PutUint64(b[6:], granule)
	binary.LittleEndian.PutUint32(b[14:], 0x6c696e78)
	binary.LittleEndian.PutUint32(b[18:], sequence)
	b[26] = byte(segments)
	remaining := len(data)
	for i := 0; i < segments; i++ {
		n := remaining
		if n > 255 {
			n = 255
		}
		b[27+i] = byte(n)
		remaining -= n
	}
	copy(b[27+segments:], data)
	var crc uint32
	for _, v := range b {
		crc ^= uint32(v) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	binary.LittleEndian.PutUint32(b[22:], crc)
	return b
}

// OpusPacketDurationMS uses the packet TOC, including packets with multiple frames.
func OpusPacketDurationMS(packet []byte) int64 { return int64(opusSamples(packet)) * 1000 / 48000 }
