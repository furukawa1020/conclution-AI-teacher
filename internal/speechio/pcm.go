package speechio

const meaningfulPCM16Magnitude = 33

// PCM16HasMeaningfulSample applies the same 0.001 full-scale floor as the web
// playback metric. Transport bytes and digital silence still cross normally,
// but they cannot satisfy the product's substantive-audio latency SLO.
func PCM16HasMeaningfulSample(pcm []byte) bool {
	for offset := 0; offset+1 < len(pcm); offset += 2 {
		sample := int(int16(uint16(pcm[offset]) | uint16(pcm[offset+1])<<8))
		if sample < 0 {
			sample = -sample
		}
		if sample >= meaningfulPCM16Magnitude {
			return true
		}
	}
	return false
}
