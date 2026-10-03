package stt

// StreamParams configures a WebSocket streaming session.
//
// VAD and Punctuate use *bool so that the zero value (nil) can default to
// true (matching the server's default behavior). Use stt.Bool(true) or
// stt.Bool(false) to set them explicitly.
type StreamParams struct {
	Language       string // default: "en"
	VAD            *bool  // default: true (nil → true)
	InterimResults bool
	SmartFormat    bool
	Punctuate      *bool  // default: true (nil → true)
	Encoding       string // "pcm_s16le" (default) or "pcm_f32le"
	SampleRate     int    // default: 16000
}

// Bool is a helper that returns a pointer to b, for use with *bool fields
// in StreamParams (e.g. stt.StreamParams{VAD: stt.Bool(false)}).
func Bool(b bool) *bool {
	return &b
}

// StreamEvent is received from the server during streaming.
type StreamEvent struct {
	Type           string         `json:"type"` // Metadata, Results, SpeechStarted, Error, CloseStream
	RequestID      string         `json:"request_id,omitempty"`
	Model          string         `json:"model,omitempty"`
	Channels       int            `json:"channels,omitempty"`
	IsFinal        bool           `json:"is_final,omitempty"`
	SpeechFinal    bool           `json:"speech_final,omitempty"`
	FromFinalize   bool           `json:"from_finalize,omitempty"`
	SpeechStartedS *float64       `json:"speech_started_s,omitempty"`
	TimestampS     float64        `json:"timestamp_s,omitempty"`
	Channel        *StreamChannel `json:"channel,omitempty"`
	Message        string         `json:"message,omitempty"`
}

// StreamChannel holds recognition alternatives for a single audio channel.
type StreamChannel struct {
	Alternatives []StreamAlternative `json:"alternatives"`
}

// StreamAlternative is one recognition hypothesis.
type StreamAlternative struct {
	Transcript string  `json:"transcript"`
	Confidence float32 `json:"confidence"`
	Words      []Word  `json:"words,omitempty"`
}

// Transcript returns the best transcript text from the event, or "" if absent.
func (e *StreamEvent) Transcript() string {
	if e.Channel == nil || len(e.Channel.Alternatives) == 0 {
		return ""
	}
	return e.Channel.Alternatives[0].Transcript
}

// StreamHandler receives streaming events from a StreamClient.
type StreamHandler interface {
	OnEvent(event StreamEvent)
	OnError(err error)
}
