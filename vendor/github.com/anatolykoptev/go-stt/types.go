package stt

// Response holds the basic transcription result.
type Response struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// Segment represents a timestamped chunk of transcribed text.
type Segment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Word represents a single transcribed word with timing and speaker info.
type Word struct {
	Word    string  `json:"word"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
}

// Utterance is a speaker-attributed block of speech (used with diarization).
type Utterance struct {
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
}

// VerboseResponse holds the full transcription result including segments, words, and utterances.
type VerboseResponse struct {
	Text               string      `json:"text"`
	Language           string      `json:"language,omitempty"`
	Duration           float64     `json:"duration,omitempty"`
	Segments           []Segment   `json:"segments,omitempty"`
	Words              []Word      `json:"words,omitempty"`
	LanguageConfidence float64     `json:"language_confidence,omitempty"`
	Utterances         []Utterance `json:"utterances,omitempty"`
}

// Model describes a single model entry returned by the /v1/models endpoint.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ModelList is the response from GET /v1/models.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}
