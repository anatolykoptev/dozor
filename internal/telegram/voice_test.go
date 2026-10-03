package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const fakeBotToken = "123456:SECRETbotTOKENvalue"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newVoiceBot builds a Bot whose tgbotapi client talks to a fake Telegram API
// (getMe/getFile) and whose voice downloads go through dl.
func newVoiceBot(t *testing.T, dl *http.Client) (*Channel, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"b","username":"b"}}`)
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"f1","file_path":"voice/a.oga"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api, err := tgbotapi.NewBotAPIWithClient(fakeBotToken, srv.URL+"/bot%s/%s", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &Channel{bot: api, sttURL: "http://127.0.0.1:1", sttLang: "en", dl: dl}, srv
}

func voiceMsg() *tgbotapi.Voice { return &tgbotapi.Voice{FileID: "f1", FileSize: 9} }

// logAsBotDoes renders err the way bot.go does when transcription fails.
func logAsBotDoes(err error) string {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	l.Error("telegram: voice transcription failed", slog.Any("error", err))
	return buf.String()
}

func TestTranscribeVoiceErrorsNeverContainBotToken(t *testing.T) {
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated transport failure")
	})}
	notFound := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}}, nil
	})}

	t.Run("download transport failure", func(t *testing.T) {
		b, _ := newVoiceBot(t, failing)
		_, err := b.transcribeVoice(context.Background(), voiceMsg())
		assertNoToken(t, err)
		if !strings.Contains(err.Error(), "simulated transport failure") {
			t.Errorf("cause lost: %v", err)
		}
	})
	t.Run("download HTTP 404", func(t *testing.T) {
		b, _ := newVoiceBot(t, notFound)
		_, err := b.transcribeVoice(context.Background(), voiceMsg())
		assertNoToken(t, err)
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("status lost: %v", err)
		}
	})
	t.Run("getFile transport failure", func(t *testing.T) {
		b, srv := newVoiceBot(t, notFound)
		srv.Close() // tgbotapi's own request now fails with a *url.Error carrying the API URL
		_, err := b.transcribeVoice(context.Background(), voiceMsg())
		assertNoToken(t, err)
	})
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error contains the bot token: %v", err)
	}
	if logged := logAsBotDoes(err); strings.Contains(logged, "SECRET") {
		t.Errorf("log line contains the bot token: %s", logged)
	}
}

func TestTranscribeVoiceLogsLengthNotTranscript(t *testing.T) {
	const transcript = "private spoken words 4471"
	sttSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"text":%q,"duration":2.5}`, transcript)
	}))
	defer sttSrv.Close()
	ok := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	b, _ := newVoiceBot(t, ok)
	b.sttURL = sttSrv.URL

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	text, err := b.transcribeVoice(context.Background(), voiceMsg())
	if err != nil || text != transcript {
		t.Fatalf("text=%q err=%v", text, err)
	}
	log := buf.String()
	if strings.Contains(log, "private") || strings.Contains(log, "4471") {
		t.Errorf("transcript logged: %s", log)
	}
	if !strings.Contains(log, "text_runes=25") {
		t.Errorf("length not logged: %s", log)
	}
}
