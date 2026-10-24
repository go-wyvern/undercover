// Average/resample mono input into the 16kHz PCM16LE frames required by Linx.
class PCMRecorder extends AudioWorkletProcessor {
  constructor() {
    super()
    this.ratio = sampleRate / 16000
    this.position = 0
    this.sum = 0
    this.count = 0
    this.samples = []
    this.active = true
    this.port.onmessage = ({ data }) => {
      if (data === 'flush') {
        this.active = false
        if (this.samples.length) this.emit(true)
        this.port.postMessage({ type: 'flushed' })
      }
    }
  }
  emit(pad = false) {
    const frame = new Int16Array(320)
    for (let i = 0; i < 320; i++) frame[i] = this.samples[i] || 0
    this.samples.splice(0, 320)
    this.port.postMessage({ type: 'pcm', buffer: frame.buffer }, [frame.buffer])
  }
  process(inputs) {
    if (!this.active || !inputs[0]?.[0]) return true
    for (const value of inputs[0][0]) {
      this.sum += value
      this.count++
      this.position++
      if (this.position >= this.ratio) {
        const sample = Math.max(-1, Math.min(1, this.sum / this.count))
        this.samples.push(Math.round(sample * (sample < 0 ? 32768 : 32767)))
        this.position -= this.ratio
        this.sum = 0
        this.count = 0
        if (this.samples.length === 320) this.emit()
      }
    }
    return true
  }
}
registerProcessor('pcm-recorder', PCMRecorder)
